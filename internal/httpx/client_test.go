package httpx

import (
	"context"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGet_TrustsExtraCA(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()
	cert, err := x509.ParseCertificate(ts.TLS.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)

	untrusted := NewLockedClient(Options{Timeout: 2 * time.Second})
	if err := untrusted.RegisterDestination("src", ts.URL); err != nil {
		t.Fatal(err)
	}
	resp, _, err := untrusted.Get(context.Background(), "src", "/", nil)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected TLS failure without extra CA")
	}

	trusted := NewLockedClient(Options{Timeout: 2 * time.Second, RootCAs: pool})
	if err := trusted.RegisterDestination("src", ts.URL); err != nil {
		t.Fatal(err)
	}
	resp, body, err := trusted.Get(context.Background(), "src", "/", nil)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "ok") {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
}

func TestGet_OK(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method %s", r.Method)
		}
		if r.URL.Path != "/api/x" {
			t.Errorf("path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	c := NewLockedClient(Options{Timeout: 2 * time.Second})
	if err := c.RegisterDestination("main", ts.URL); err != nil {
		t.Fatal(err)
	}
	// LockedClient.Get closes the body before returning the response.
	resp, body, err := c.Get(context.Background(), "main", "/api/x", nil) //nolint:bodyclose
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	if string(body) != `{"ok":true}` {
		t.Fatalf("%s", body)
	}
}

func TestGet_RejectsAbsolutePath(t *testing.T) {
	c := NewLockedClient(Options{})
	_ = c.RegisterDestination("main", "https://example.internal")
	// LockedClient.Get closes the body before returning the response.
	_, _, err := c.Get(context.Background(), "main", "https://evil.example/x", nil) //nolint:bodyclose
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestGet_UnknownDest(t *testing.T) {
	c := NewLockedClient(Options{})
	// LockedClient.Get closes the body before returning the response.
	_, _, err := c.Get(context.Background(), "nope", "/x", nil) //nolint:bodyclose
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestGet_RedirectDisabled(t *testing.T) {
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("secret-meta"))
	}))
	defer final.Close()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL+"/latest/meta-data", http.StatusFound)
	}))
	defer ts.Close()

	c := NewLockedClient(Options{Timeout: time.Second})
	if err := c.RegisterDestination("main", ts.URL); err != nil {
		t.Fatal(err)
	}
	_, body, err := c.Get(context.Background(), "main", "/start", nil) //nolint:bodyclose
	if err == nil {
		t.Fatalf("expected redirect error, body=%s", body)
	}
	if strings.Contains(err.Error(), "secret-meta") {
		t.Fatalf("leaked body: %v", err)
	}
}

func TestGet_NetworkErrorDoesNotExposeHost(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	rawURL := ts.URL
	ts.Close()
	c := NewLockedClient(Options{Timeout: time.Second})
	if err := c.RegisterDestination("main", rawURL); err != nil {
		t.Fatal(err)
	}
	_, _, err := c.Get(context.Background(), "main", "/x", nil) //nolint:bodyclose
	if err == nil {
		t.Fatal("expected network error")
	}
	host := strings.TrimPrefix(rawURL, "http://")
	if strings.Contains(err.Error(), host) || !strings.Contains(err.Error(), "main") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestGet_BodyLimit(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("a", 1000))
	}))
	defer ts.Close()
	c := NewLockedClient(Options{Timeout: time.Second, MaxBody: 100})
	_ = c.RegisterDestination("main", ts.URL)
	_, body, err := c.Get(context.Background(), "main", "/", nil) //nolint:bodyclose
	if err == nil {
		t.Fatal("expected body limit error")
	}
	if len(body) > 100 {
		t.Fatalf("body len %d", len(body))
	}
}

func TestRegister_RejectsUserinfo(t *testing.T) {
	c := NewLockedClient(Options{})
	err := c.RegisterDestination("x", "https://user:pass@example.internal")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRegister_RejectsBadScheme(t *testing.T) {
	c := NewLockedClient(Options{})
	if err := c.RegisterDestination("x", "ftp://example.internal"); err == nil {
		t.Fatal("expected error")
	}
}

func TestRegister_RejectsQueryAndFragment(t *testing.T) {
	c := NewLockedClient(Options{})
	for _, raw := range []string{
		"https://example.internal/api?tenant=lab",
		"https://example.internal/api#fragment",
	} {
		if err := c.RegisterDestination("x", raw); err == nil {
			t.Fatalf("expected rejection for %q", raw)
		}
	}
}

func TestRegisterDestination_RejectsUnsafeBasePath(t *testing.T) {
	for _, raw := range []string{
		"https://example.internal/api/../admin",
		"https://example.internal/api/%2e/admin",
		"https://example.internal/api/%5cadmin",
	} {
		c := NewLockedClient(Options{})
		if err := c.RegisterDestination("x", raw); err == nil {
			t.Fatalf("destination accepted: %q", raw)
		}
	}
}

func TestGet_RejectsPathTraversal(t *testing.T) {
	c := NewLockedClient(Options{})
	_ = c.RegisterDestination("main", "https://example.internal/prefix")
	for _, path := range []string{
		"/../admin", "/safe/../admin", "/safe/%2e/admin", "//evil.example/x",
	} {
		// LockedClient.Get closes the body before returning the response.
		//nolint:bodyclose
		if _, _, err := c.Get(context.Background(), "main", path, nil); err == nil {
			t.Fatalf("expected rejection for %q", path)
		}
	}
}

func TestGet_RejectsOversizedPath(t *testing.T) {
	c := NewLockedClient(Options{})
	if _, _, err := c.Get( //nolint:bodyclose
		context.Background(), "main", "/"+strings.Repeat("x", 8192), nil,
	); err == nil || !strings.Contains(err.Error(), "8192") {
		t.Fatalf("error=%v", err)
	}
}

func TestGet_CacheHit(t *testing.T) {
	var n int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()
	c := NewLockedClient(Options{Timeout: time.Second, CacheTTL: time.Minute})
	_ = c.RegisterDestination("main", ts.URL)
	firstResp, _, err := c.Get(context.Background(), "main", "/x", nil) //nolint:bodyclose
	if err != nil {
		t.Fatal(err)
	}
	fallback := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := CollectionTime(firstResp, fallback); !got.Equal(fallback) {
		t.Fatalf("direct response must not carry a cache time: %s", got)
	}
	beforeHit := time.Now().UTC().Add(-time.Second)
	cachedResp, _, err := c.Get(context.Background(), "main", "/x", nil) //nolint:bodyclose
	if err != nil {
		t.Fatal(err)
	}
	collectedAt := CollectionTime(cachedResp, fallback)
	if collectedAt.Equal(fallback) || collectedAt.Before(beforeHit) || collectedAt.After(time.Now().UTC()) {
		t.Fatalf("invalid cache time: %s", collectedAt)
	}
	if n != 1 {
		t.Fatalf("expected 1 upstream call, got %d", n)
	}
	st := c.Stats()
	if st.Hits < 1 || st.Misses < 1 {
		t.Fatalf("%+v", st)
	}
}

func TestStats_ExcludesExpiredEntries(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer ts.Close()
	c := NewLockedClient(Options{Timeout: time.Second, CacheTTL: time.Minute})
	if err := c.RegisterDestination("main", ts.URL); err != nil {
		t.Fatal(err)
	}
	// LockedClient.Get closes the body before returning the response.
	//nolint:bodyclose
	if _, _, err := c.Get(context.Background(), "main", "/x", nil); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	for key, entry := range c.cache {
		entry.expiresAt = time.Now().Add(-time.Second)
		c.cache[key] = entry
	}
	c.mu.Unlock()
	if got := c.Stats().Size; got != 0 {
		t.Fatalf("expired entries counted: %d", got)
	}
	if got := c.Stats().Bytes; got != 0 {
		t.Fatalf("expired bytes counted: %d", got)
	}
}

func TestCachePut_EvictsOldestFirst(t *testing.T) {
	c := NewLockedClient(Options{CacheTTL: time.Minute})
	chunk := make([]byte, (maxCacheBytes/2)+1)
	c.cachePut("oldest", http.StatusOK, chunk, time.Now())
	c.cachePut("newest", http.StatusOK, chunk, time.Now())
	if _, _, _, ok := c.cacheGet("oldest"); ok {
		t.Fatal("oldest entry should be evicted first")
	}
	if _, _, _, ok := c.cacheGet("newest"); !ok {
		t.Fatal("newest entry should remain")
	}
}

func TestCachePut_BoundsCumulativeBytes(t *testing.T) {
	c := NewLockedClient(Options{CacheTTL: time.Minute})
	first := make([]byte, (maxCacheBytes/2)+1)
	second := make([]byte, (maxCacheBytes/2)+1)
	c.cachePut("first", http.StatusOK, first, time.Now())
	c.cachePut("second", http.StatusOK, second, time.Now())
	stats := c.Stats()
	if stats.Bytes > maxCacheBytes || stats.Size != 1 {
		t.Fatalf("cache not bounded: %+v", stats)
	}
}

func TestGet_PreservesBasePathPrefix(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.URL.RawQuery != "all=true" {
			t.Errorf("query %q", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer ts.Close()

	// Simulate a mount behind a reverse proxy.
	base := strings.TrimRight(ts.URL, "/") + "/gatus"
	c := NewLockedClient(Options{Timeout: 2 * time.Second})
	if err := c.RegisterDestination("gatus", base); err != nil {
		t.Fatal(err)
	}
	resp, body, err := c.Get(context.Background(), "gatus", "/api/v1/endpoints/statuses?all=true", nil) //nolint:bodyclose
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status %d body=%s", resp.StatusCode, body)
	}
	want := "/gatus/api/v1/endpoints/statuses"
	if gotPath != want {
		t.Fatalf("path=%q want %q (base path prefix must not be stripped)", gotPath, want)
	}
}

func TestJoinURLPath(t *testing.T) {
	cases := []struct {
		base, rel, want string
	}{
		{"", "/api/x", "/api/x"},
		{"/gatus", "/api/x", "/gatus/api/x"},
		{"/gatus/", "/api/x", "/gatus/api/x"},
		{"/proxy/v1", "/containers/json", "/proxy/v1/containers/json"},
	}
	for _, tc := range cases {
		got := joinURLPath(tc.base, tc.rel)
		if got != tc.want {
			t.Errorf("joinURLPath(%q,%q)=%q want %q", tc.base, tc.rel, got, tc.want)
		}
	}
}

func TestHeaderFingerprint_RedactsSecretValues(t *testing.T) {
	const secret = "super-secret-token-value-XYZ-999"
	fp := headerFingerprint(map[string]string{
		"Authorization":  "Bearer " + secret,
		"X-Api-Key":      secret,
		"X-Auth-Token":   secret,
		"X-Custom-Token": secret,
		"X-Scope-OrgID":  "lab-tenant",
		"Accept":         "application/json",
	}, []byte("test-key"))
	if strings.Contains(fp, secret) {
		t.Fatalf("fingerprint embeds cleartext secret: %q", fp)
	}
	if strings.Contains(fp, "Bearer ") {
		t.Fatalf("fingerprint embeds bearer material: %q", fp)
	}
	// All values are masked: token_header accepts arbitrary names.
	if strings.Contains(fp, "lab-tenant") || strings.Contains(fp, "application/json") {
		t.Fatalf("fingerprint embeds cleartext header value: %q", fp)
	}
	if !strings.Contains(fp, "authorization=<keyed:") {
		t.Fatalf("expected authorization keyed marker: %q", fp)
	}
	if !strings.Contains(fp, "x-auth-token=<keyed:") {
		t.Fatalf("expected custom token header keyed marker: %q", fp)
	}
	if !strings.Contains(fp, "x-custom-token=<keyed:") {
		t.Fatalf("expected x-custom-token keyed marker: %q", fp)
	}
	if !strings.Contains(fp, "x-scope-orgid=<keyed:") {
		t.Fatalf("expected non-secret header keyed marker: %q", fp)
	}
}

func TestHeaderFingerprint_DistinguishesSecretValues(t *testing.T) {
	key := []byte("test-key")
	a := headerFingerprint(map[string]string{"Authorization": "Bearer first"}, key)
	b := headerFingerprint(map[string]string{"Authorization": "Bearer second"}, key)
	if a == b {
		t.Fatal("different credentials must not share a cache fingerprint")
	}
}

func TestGet_CacheKeyDoesNotLeakCustomTokenHeader(t *testing.T) {
	const secret = "cache-key-secret-value-should-not-appear"
	var n int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()
	c := NewLockedClient(Options{Timeout: time.Second, CacheTTL: time.Minute})
	_ = c.RegisterDestination("main", ts.URL)
	hdrs := map[string]string{"X-Auth-Token": secret}
	// LockedClient.Get closes the body before returning the response.
	//nolint:bodyclose
	if _, _, err := c.Get(context.Background(), "main", "/cached", hdrs); err != nil {
		t.Fatal(err)
	}
	// The same path and headers must reuse the cache.
	// LockedClient.Get closes the body before returning the response.
	//nolint:bodyclose
	if _, _, err := c.Get(context.Background(), "main", "/cached", hdrs); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected cache hit (1 upstream), got %d", n)
	}
	// Verify no local cache key contains the secret in cleartext.
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.cache {
		if strings.Contains(k, secret) {
			t.Fatalf("cache key contains cleartext secret: %q", k)
		}
	}
}

func TestGet_CacheKeyDoesNotLeakQueryText(t *testing.T) {
	const secret = "search-secret-value"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()
	c := NewLockedClient(Options{Timeout: time.Second, CacheTTL: time.Minute})
	_ = c.RegisterDestination("main", ts.URL)
	if _, _, err := c.Get( //nolint:bodyclose
		context.Background(), "main", "/search?query="+secret, nil,
	); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.cache {
		if strings.Contains(key, secret) || strings.Contains(key, "query=") {
			t.Fatalf("cache key contains request text: %q", key)
		}
	}
}
