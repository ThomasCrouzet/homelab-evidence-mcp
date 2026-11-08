// Package httpx gives adapters a GET-only locked HTTP client.
package httpx

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

const (
	defaultMaxBody  = 2 << 20 // 2 MiB
	maxCacheBytes   = 32 << 20
	maxCacheEntries = 128
	cacheTimeHeader = "X-Homelab-Evidence-Cache-Time"
	// UpstreamTruncatedHeader is the ntfy replay-limit signal. It is the only
	// upstream header retained by the source cache.
	UpstreamTruncatedHeader = "X-Messages-Truncated"
)

// ErrBodyTooLarge reports a response body above the configured cap. The
// returned body contains the first MaxBody bytes so a caller can keep
// complete records that arrived before the cutoff.
var ErrBodyTooLarge = errors.New("response body exceeds configured limit")

// destination contains a base URL set at startup.
type destination struct {
	BaseURL *url.URL
}

// CacheStats gives data about local GET response cache usage.
type CacheStats struct {
	Hits      uint64 `json:"hits"`
	Misses    uint64 `json:"misses"`
	Coalesced uint64 `json:"coalesced"`
	Size      int    `json:"size"`
	Bytes     int64  `json:"bytes"`
	TTL       string `json:"ttl"`
}

type flight struct {
	done        chan struct{}
	status      int
	header      http.Header
	body        []byte
	collectedAt time.Time
	truncated   bool
	err         error
}

type cacheEntry struct {
	status      int
	body        []byte
	collectedAt time.Time
	expiresAt   time.Time
	truncated   bool
}

// LockedClient sends GET requests only to destinations in its registry.
type LockedClient struct {
	mu           sync.RWMutex
	destinations map[string]*destination
	client       *http.Client
	maxBody      int64
	userAgent    string
	cacheTTL     time.Duration
	cache        map[string]cacheEntry
	cacheOrder   []string
	cacheBytes   int64
	cacheKey     [32]byte
	flightMu     sync.Mutex
	flights      map[string]*flight
	hits         atomic.Uint64
	misses       atomic.Uint64
	coalesced    atomic.Uint64
	now          func() time.Time
}

func (c *LockedClient) clock() time.Time {
	if c.now != nil {
		return c.now().UTC()
	}
	return time.Now().UTC()
}

// Options contains the client settings.
type Options struct {
	Timeout   time.Duration
	MaxBody   int64
	UserAgent string
	// CacheTTL sets the cache duration for the same GET requests. Zero deactivates caching.
	CacheTTL time.Duration
	// RootCAs contains the trust pool with additional certificates. TLS certificate verification is always active.
	// The client does not give a skip-verify option.
	RootCAs *x509.CertPool
	// Now supplies the clock for snapshot collection times. Nil uses time.Now.
	Now func() time.Time
	// AllowedCIDRs limits every dial to an address in one of these networks.
	// An empty slice keeps the normal resolver and dial behavior.
	AllowedCIDRs []*net.IPNet
}

// NewLockedClient makes a client with TLS certificate verification. Redirects cannot occur.
func NewLockedClient(opts Options) *LockedClient {
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	if opts.MaxBody <= 0 {
		opts.MaxBody = defaultMaxBody
	}
	if opts.UserAgent == "" {
		opts.UserAgent = "homelab-evidence-mcp/0.1"
	}
	dialer := &net.Dialer{
		Timeout:   5 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	dialContext := dialer.DialContext
	if len(opts.AllowedCIDRs) > 0 {
		allowed := opts.AllowedCIDRs
		dialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, errors.New("network address rejected")
			}
			var addresses []net.IPAddr
			if parsed := net.ParseIP(host); parsed != nil {
				addresses = []net.IPAddr{{IP: parsed}}
			} else {
				addresses, err = net.DefaultResolver.LookupIPAddr(ctx, host)
				if err != nil {
					return nil, errors.New("destination resolution failed")
				}
			}
			for _, candidate := range addresses {
				if !addressAllowed(candidate.IP, allowed) {
					continue
				}
				return dialer.DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
			}
			return nil, errors.New("destination resolved outside the allowed address policy")
		}
	}
	transport := &http.Transport{
		Proxy:               nil, // Do not use HTTP_PROXY for adapter traffic.
		DialContext:         dialContext,
		TLSHandshakeTimeout: 5 * time.Second,
		MaxIdleConns:        16,
		IdleConnTimeout:     60 * time.Second,
		ForceAttemptHTTP2:   true,
	}
	if opts.RootCAs != nil {
		transport.TLSClientConfig = &tls.Config{
			RootCAs:    opts.RootCAs,
			MinVersion: tls.VersionTLS12,
		}
	}
	lc := &LockedClient{
		destinations: make(map[string]*destination),
		maxBody:      opts.MaxBody,
		userAgent:    opts.UserAgent,
		cacheTTL:     opts.CacheTTL,
		now:          opts.Now,
		cache:        make(map[string]cacheEntry),
		flights:      make(map[string]*flight),
	}
	if _, err := rand.Read(lc.cacheKey[:]); err != nil {
		// If there is no random key, deactivate the cache. This prevents a weak authentication fingerprint.
		lc.cacheTTL = 0
	}
	lc.client = &http.Client{
		Transport: transport,
		Timeout:   opts.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return errRedirectsDisabled
		},
	}
	return lc
}

var (
	errRedirectsDisabled = errors.New("http redirects are disabled")
	errUnknownDest       = errors.New("unknown destination")
	errHostMismatch      = errors.New("resolved request host is outside locked destination")
)

// RegisterDestination locks a named URL at startup.
// RegisterDestination keeps path prefixes when it joins routes.
func (c *LockedClient) RegisterDestination(name, rawURL string) error {
	if name == "" {
		return errors.New("destination name is required")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("destination %q: parse url: %w", name, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("destination %q: scheme %q not allowed (http/https only)", name, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("destination %q: host is required", name)
	}
	if u.User != nil {
		return fmt.Errorf("destination %q: userinfo in URL is not allowed", name)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("destination %q: query strings and fragments are not allowed", name)
	}
	if len([]rune(rawURL)) > 4096 || invalidPath(u.Path) {
		return fmt.Errorf("destination %q: invalid or oversized base path", name)
	}
	// Keep the prefix and remove trailing slashes for stable composition.
	basePath := strings.TrimRight(u.Path, "/")
	base := &url.URL{
		Scheme: u.Scheme,
		Host:   u.Host,
		Path:   basePath,
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.destinations[name]; exists {
		return fmt.Errorf("destination %q already registered", name)
	}
	c.destinations[name] = &destination{BaseURL: base}
	return nil
}

// joinURLPath joins a locked prefix and a relative path.
// rel starts with /. If the prefix is empty, rel does not change.
func joinURLPath(basePath, rel string) string {
	basePath = strings.TrimRight(basePath, "/")
	if !strings.HasPrefix(rel, "/") {
		rel = "/" + rel
	}
	if basePath == "" {
		return rel
	}
	return basePath + rel
}

func invalidPath(path string) bool {
	if strings.Contains(path, `\`) {
		return true
	}
	for _, r := range path {
		if unicode.IsControl(r) {
			return true
		}
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return true
		}
	}
	return false
}

// Stats gives cache counters.
func (c *LockedClient) Stats() CacheStats {
	c.mu.Lock()
	now := time.Now()
	c.expireCacheLocked(now)
	size := len(c.cache)
	bytes := c.cacheBytes
	ttl := c.cacheTTL
	c.mu.Unlock()
	return CacheStats{
		Hits:      c.hits.Load(),
		Misses:    c.misses.Load(),
		Coalesced: c.coalesced.Load(),
		Size:      size,
		Bytes:     bytes,
		TTL:       ttl.String(),
	}
}

// Get sends a GET request to a destination in the registry and a relative path.
// The path must start with /. It can include a query string.
func (c *LockedClient) Get(ctx context.Context, destName, path string, headers map[string]string) (*http.Response, []byte, error) {
	if ctx == nil {
		return nil, nil, errors.New("nil context")
	}
	if err := ctx.Err(); err != nil {
		// A canceled caller must not get a successful cached snapshot.
		return nil, nil, err
	}
	if !strings.HasPrefix(path, "/") {
		return nil, nil, fmt.Errorf("path must start with /")
	}
	if len(path) > 8192 {
		return nil, nil, fmt.Errorf("path exceeds 8192 bytes")
	}
	c.mu.RLock()
	dest, ok := c.destinations[destName]
	c.mu.RUnlock()
	if !ok {
		return nil, nil, fmt.Errorf("%w: %s", errUnknownDest, destName)
	}

	rel, err := url.Parse(path)
	if err != nil {
		return nil, nil, errors.New("invalid request path")
	}
	if rel.IsAbs() || rel.Host != "" || rel.Scheme != "" {
		return nil, nil, errors.New("absolute URLs in path are not allowed")
	}
	if rel.Fragment != "" {
		return nil, nil, errors.New("fragments in path are not allowed")
	}
	if strings.HasPrefix(rel.Path, "//") || invalidPath(rel.Path) {
		return nil, nil, errors.New("invalid relative path")
	}
	// Compose the URL here. ResolveReference removes the prefix from a path that starts with /.
	full := &url.URL{
		Scheme:   dest.BaseURL.Scheme,
		Host:     dest.BaseURL.Host,
		Path:     joinURLPath(dest.BaseURL.Path, rel.Path),
		RawQuery: rel.RawQuery,
	}
	if full.Scheme != dest.BaseURL.Scheme || full.Host != dest.BaseURL.Host {
		return nil, nil, errHostMismatch
	}

	cacheKey := destName + "\n" + keyedFingerprint("path", path, c.cacheKey[:]) +
		"\n" + headerFingerprint(headers, c.cacheKey[:])
	info := requestInfoFrom(ctx)
	if c.cacheTTL > 0 {
		if body, status, collectedAt, truncated, ok := c.cacheGet(cacheKey); ok {
			c.hits.Add(1)
			info.recordHit(collectedAt)
			header := http.Header{
				cacheTimeHeader: []string{collectedAt.Format(time.RFC3339Nano)},
			}
			if truncated {
				header.Set(UpstreamTruncatedHeader, "1")
			}
			resp := &http.Response{
				StatusCode: status,
				Header:     header,
				Body:       http.NoBody,
			}
			return resp, body, nil
		}
		c.misses.Add(1)
		info.recordMiss()
	} else {
		info.recordDisabled()
	}

	for {
		c.flightMu.Lock()
		if existing, ok := c.flights[cacheKey]; ok {
			c.flightMu.Unlock()
			select {
			case <-existing.done:
				if existing.err != nil {
					// A canceled or failed leader must not poison other callers.
					if err := ctx.Err(); err != nil {
						return nil, nil, err
					}
					continue
				}
				c.coalesced.Add(1)
				info.recordCoalesced(existing.collectedAt)
				header := existing.header.Clone()
				return &http.Response{StatusCode: existing.status, Header: header, Body: http.NoBody}, existing.body, nil
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		}
		current := &flight{done: make(chan struct{})}
		c.flights[cacheKey] = current
		c.flightMu.Unlock()

		resp, body, fetchErr := c.fetch(ctx, destName, full, headers, cacheKey, info)
		if fetchErr != nil {
			current.err = fetchErr
		} else {
			current.status = resp.StatusCode
			current.header = resp.Header.Clone()
			current.body = body
			current.collectedAt = CollectionTime(resp, c.clock())
			current.truncated = headerFlag(resp.Header, UpstreamTruncatedHeader)
		}
		c.flightMu.Lock()
		delete(c.flights, cacheKey)
		c.flightMu.Unlock()
		close(current.done)
		return resp, body, fetchErr
	}
}

func (c *LockedClient) fetch(ctx context.Context, destName string, full *url.URL, headers map[string]string, cacheKey string, info *RequestInfo) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full.String(), nil)
	if err != nil {
		return nil, nil, errors.New("request construction failed")
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		canonical := http.CanonicalHeaderKey(strings.TrimSpace(k))
		lk := strings.ToLower(canonical)
		if lk == "host" {
			continue
		}
		if lk == "authorization" && strings.TrimSpace(v) == "" {
			continue
		}
		req.Header.Set(canonical, v)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, nil, sanitizeNetErr(err, destName)
	}
	defer func() { _ = resp.Body.Close() }()
	// A remote server cannot set cache metadata.
	resp.Header.Del(cacheTimeHeader)

	limited := io.LimitReader(resp.Body, c.maxBody+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return resp, nil, errors.New("response body read failed")
	}
	if int64(len(body)) > c.maxBody {
		return resp, body[:c.maxBody], fmt.Errorf("%w: %d bytes", ErrBodyTooLarge, c.maxBody)
	}
	if strings.EqualFold(strings.TrimSpace(resp.Header.Get("Content-Encoding")), "gzip") {
		reader, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return resp, nil, errors.New("response decompression failed")
		}
		decoded, readErr := io.ReadAll(io.LimitReader(reader, c.maxBody+1))
		_ = reader.Close()
		if readErr != nil {
			return resp, nil, errors.New("response decompression failed")
		}
		if int64(len(decoded)) > c.maxBody {
			return resp, nil, fmt.Errorf("%w: %d decompressed bytes", ErrBodyTooLarge, c.maxBody)
		}
		body = decoded
		resp.Header.Del("Content-Encoding")
		resp.Header.Del("Content-Length")
	}
	// One collection time describes this snapshot for the direct response and
	// every later cache hit. The header is internal and set after remote headers
	// are removed. CollectionTime reads it in both paths.
	collectedAt := c.clock()
	resp.Header.Set(cacheTimeHeader, collectedAt.Format(time.RFC3339Nano))

	if c.cacheTTL > 0 && resp.StatusCode == http.StatusOK {
		if lifetime, storable := sourceCacheLifetime(resp.Header, c.cacheTTL); storable && lifetime > 0 {
			c.cachePutWithTTL(cacheKey, resp.StatusCode, body, collectedAt, lifetime, headerFlag(resp.Header, UpstreamTruncatedHeader))
		}
	}
	return resp, body, nil
}

func addressAllowed(ip net.IP, allowed []*net.IPNet) bool {
	for _, network := range allowed {
		if network != nil && network.Contains(ip) {
			return true
		}
	}
	return false
}

// sourceCacheLifetime applies the origin cache directives. no-store and
// no-cache bypass storage. A max-age value caps the operator TTL.
func sourceCacheLifetime(header http.Header, ttl time.Duration) (time.Duration, bool) {
	directives := parseCacheControl(header)
	if _, ok := directives["no-store"]; ok {
		return 0, false
	}
	if _, ok := directives["no-cache"]; ok {
		return 0, false
	}
	if _, ok := directives["private"]; ok {
		// This is a private in-process cache. Private responses are safe here.
	}
	if value, ok := directives["max-age"]; ok {
		seconds, err := strconv.Atoi(value)
		if err != nil || seconds < 0 {
			return 0, false
		}
		maxAge := time.Duration(seconds) * time.Second
		if maxAge > ttl {
			maxAge = ttl
		}
		if maxAge <= 0 {
			return 0, false
		}
		return maxAge, true
	}
	if strings.Contains(strings.ToLower(strings.Join(header.Values("Pragma"), ",")), "no-cache") {
		return 0, false
	}
	return ttl, true
}

func parseCacheControl(header http.Header) map[string]string {
	out := map[string]string{}
	for _, raw := range header.Values("Cache-Control") {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			key, value, found := strings.Cut(part, "=")
			key = strings.ToLower(strings.TrimSpace(key))
			if !found {
				out[key] = ""
				continue
			}
			out[key] = strings.Trim(strings.TrimSpace(value), `"`)
		}
	}
	return out
}

func headerFlag(header http.Header, name string) bool {
	value := strings.TrimSpace(header.Get(name))
	return value == "1" || strings.EqualFold(value, "true")
}

// UpstreamTruncated reports the ntfy replay-limit signal on a direct or cached
// response.
func UpstreamTruncated(resp *http.Response) bool {
	return resp != nil && headerFlag(resp.Header, UpstreamTruncatedHeader)
}

func headerFingerprint(h map[string]string, key []byte) string {
	if len(h) == 0 {
		return ""
	}
	// Authentication values use a process-local HMAC fingerprint.
	// The fingerprint is different for each secret and does not store the secret.
	parts := make([]string, 0, len(h))
	for k, v := range h {
		lk := strings.ToLower(strings.TrimSpace(k))
		parts = append(parts, lk+"="+keyedFingerprint(lk, v, key))
	}
	sort.Strings(parts)
	return strings.Join(parts, "&")
}

func keyedFingerprint(label, value string, key []byte) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(label))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(value))
	return "<keyed:" + hex.EncodeToString(mac.Sum(nil)[:12]) + ">"
}

func (c *LockedClient) cacheGet(key string) ([]byte, int, time.Time, bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.cache[key]
	if !ok {
		return nil, 0, time.Time{}, false, false
	}
	if time.Now().After(e.expiresAt) {
		c.removeCacheLocked(key)
		return nil, 0, time.Time{}, false, false
	}
	body := make([]byte, len(e.body))
	copy(body, e.body)
	return body, e.status, e.collectedAt, e.truncated, true
}

func (c *LockedClient) cachePut(key string, status int, body []byte, collectedAt time.Time, truncated bool) {
	c.cachePutWithTTL(key, status, body, collectedAt, c.cacheTTL, truncated)
}

func (c *LockedClient) cachePutWithTTL(key string, status int, body []byte, collectedAt time.Time, ttl time.Duration, truncated bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	bodyBytes := int64(len(body))
	if bodyBytes > maxCacheBytes {
		return
	}
	now := time.Now()
	c.expireCacheLocked(now)
	if _, ok := c.cache[key]; ok {
		c.removeCacheLocked(key)
	}
	for len(c.cache) >= maxCacheEntries || c.cacheBytes+bodyBytes > maxCacheBytes {
		if len(c.cacheOrder) == 0 {
			break
		}
		c.removeCacheLocked(c.cacheOrder[0])
	}
	cp := make([]byte, len(body))
	copy(cp, body)
	c.cache[key] = cacheEntry{
		status:      status,
		body:        cp,
		collectedAt: collectedAt.UTC(),
		expiresAt:   now.Add(ttl),
		truncated:   truncated,
	}
	c.cacheOrder = append(c.cacheOrder, key)
	c.cacheBytes += bodyBytes
}

func (c *LockedClient) expireCacheLocked(now time.Time) {
	for key, entry := range c.cache {
		if !now.Before(entry.expiresAt) {
			c.removeCacheLocked(key)
		}
	}
}

func (c *LockedClient) removeCacheLocked(key string) {
	e, ok := c.cache[key]
	if !ok {
		return
	}
	c.cacheBytes -= int64(len(e.body))
	delete(c.cache, key)
	n := 0
	for _, k := range c.cacheOrder {
		if k != key {
			c.cacheOrder[n] = k
			n++
		}
	}
	c.cacheOrder = c.cacheOrder[:n]
}

// CollectionTime gives the collection time of a snapshot response.
// A successful response carries the time of its body read. A cache hit carries
// the initial time of the cached snapshot. Fallback applies only when the
// response has no internal timestamp.
func CollectionTime(resp *http.Response, fallback time.Time) time.Time {
	fallback = fallback.UTC()
	if resp == nil {
		return fallback
	}
	raw := resp.Header.Get(cacheTimeHeader)
	if raw == "" {
		return fallback
	}
	collectedAt, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return fallback
	}
	return collectedAt.UTC()
}

func sanitizeNetErr(err error, destination string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, errRedirectsDisabled) || strings.Contains(err.Error(), "redirect") {
		return fmt.Errorf("request to %s failed: redirects disabled or rejected", destination)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("request to %s failed: timeout", destination)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return fmt.Errorf("request to %s failed: timeout", destination)
	}
	return fmt.Errorf("request to %s failed: network or TLS error", destination)
}

// CombinedStats sums cache counters across clients.
func CombinedStats(clients ...*LockedClient) CacheStats {
	var out CacheStats
	for _, client := range clients {
		if client == nil {
			continue
		}
		stats := client.Stats()
		out.Hits += stats.Hits
		out.Misses += stats.Misses
		out.Size += stats.Size
		out.Bytes += stats.Bytes
		if out.TTL == "" {
			out.TTL = stats.TTL
		}
	}
	return out
}
