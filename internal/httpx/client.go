// Package httpx gives adapters a GET-only locked HTTP client.
package httpx

import (
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
)

// destination contains a base URL set at startup.
type destination struct {
	BaseURL *url.URL
}

// CacheStats gives data about local GET response cache usage.
type CacheStats struct {
	Hits   uint64 `json:"hits"`
	Misses uint64 `json:"misses"`
	Size   int    `json:"size"`
	Bytes  int64  `json:"bytes"`
	TTL    string `json:"ttl"`
}

type cacheEntry struct {
	status      int
	body        []byte
	collectedAt time.Time
	expiresAt   time.Time
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
	hits         atomic.Uint64
	misses       atomic.Uint64
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
	transport := &http.Transport{
		Proxy: nil, // Do not use HTTP_PROXY for adapter traffic.
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
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
		cache:        make(map[string]cacheEntry),
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
		Hits:   c.hits.Load(),
		Misses: c.misses.Load(),
		Size:   size,
		Bytes:  bytes,
		TTL:    ttl.String(),
	}
}

// Get sends a GET request to a destination in the registry and a relative path.
// The path must start with /. It can include a query string.
func (c *LockedClient) Get(ctx context.Context, destName, path string, headers map[string]string) (*http.Response, []byte, error) {
	if ctx == nil {
		return nil, nil, errors.New("nil context")
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
	if c.cacheTTL > 0 {
		if body, status, collectedAt, ok := c.cacheGet(cacheKey); ok {
			c.hits.Add(1)
			resp := &http.Response{
				StatusCode: status,
				Header: http.Header{
					cacheTimeHeader: []string{collectedAt.Format(time.RFC3339Nano)},
				},
				Body: http.NoBody,
			}
			return resp, body, nil
		}
		c.misses.Add(1)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full.String(), nil)
	if err != nil {
		return nil, nil, errors.New("request construction failed")
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		lk := strings.ToLower(k)
		if lk == "host" {
			continue
		}
		if lk == "authorization" && strings.TrimSpace(v) == "" {
			continue
		}
		req.Header.Set(k, v)
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
		return resp, body[:c.maxBody], fmt.Errorf("response body exceeds %d bytes", c.maxBody)
	}

	if c.cacheTTL > 0 && resp.StatusCode == http.StatusOK {
		c.cachePut(cacheKey, resp.StatusCode, body, time.Now().UTC())
	}
	return resp, body, nil
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

func (c *LockedClient) cacheGet(key string) ([]byte, int, time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.cache[key]
	if !ok {
		return nil, 0, time.Time{}, false
	}
	if time.Now().After(e.expiresAt) {
		c.removeCacheLocked(key)
		return nil, 0, time.Time{}, false
	}
	body := make([]byte, len(e.body))
	copy(body, e.body)
	return body, e.status, e.collectedAt, true
}

func (c *LockedClient) cachePut(key string, status int, body []byte, collectedAt time.Time) {
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
		expiresAt:   time.Now().Add(c.cacheTTL),
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

// CollectionTime gives the initial collection time of a cached response.
// CollectionTime keeps fallback for a direct network response.
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
