package healthchecks

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/httpx"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/redaction"
)

// quotaClock is a mutable clock for the cache lifetime and the sliding budget.
type quotaClock struct {
	mu sync.Mutex
	at time.Time
}

func newQuotaClock() *quotaClock {
	return &quotaClock{at: time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)}
}

func (c *quotaClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *quotaClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

func newQuotaClient(t *testing.T, baseURL string, cacheTTL time.Duration, clock *quotaClock) *Client {
	t.Helper()
	client := httpx.NewLockedClient(httpx.Options{Timeout: 2 * time.Second, CacheTTL: cacheTTL, Now: clock.Now})
	if err := client.RegisterDestination("hc", baseURL); err != nil {
		t.Fatal(err)
	}
	eng, err := redaction.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &Client{
		HTTP:     client,
		DestName: "hc",
		Headers:  map[string]string{"X-Api-Key": "quota-fixture-token"},
		Redact:   eng,
		Now:      clock.Now,
	}
}

// quotaUsed reads the sliding-window counter without changing it.
func quotaUsed(cli *Client) int {
	cli.requestMu.Lock()
	defer cli.requestMu.Unlock()
	return len(cli.requestTimes)
}

func waitForQuotaState(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func quotaListHandler(calls *atomic.Int32, check any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v3/checks/" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"checks": []any{check}})
	}
}

func quotaCheck(name, slug, key string) map[string]any {
	return map[string]any{
		"name": name, "slug": slug, "status": "up", "tags": "ops",
		"unique_key": key,
	}
}

func TestFetch_WarmCacheDoesNotConsumeRequestBudget(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(quotaListHandler(&calls, quotaCheck("backup", "backup", strings.Repeat("a", 32))))
	defer server.Close()
	clock := newQuotaClock()
	cli := newQuotaClient(t, server.URL, time.Minute, clock)
	cli.RequestLimit = 1

	for attempt := 1; attempt <= 2; attempt++ {
		snapshot, err := cli.Fetch(context.Background())
		if err != nil {
			t.Fatalf("fetch %d: %v", attempt, err)
		}
		if len(snapshot.checks) != 1 {
			t.Fatalf("fetch %d checks=%d, want 1", attempt, len(snapshot.checks))
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream requests=%d, want 1 for a warm cache hit", got)
	}
	if got := quotaUsed(cli); got != 1 {
		t.Fatalf("budget slots used=%d, want 1 for one upstream request", got)
	}
}

func TestFetch_ExhaustedBudgetBlocksColdRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(quotaListHandler(&calls, quotaCheck("backup", "backup", strings.Repeat("a", 32))))
	defer server.Close()
	clock := newQuotaClock()
	cli := newQuotaClient(t, server.URL, 0, clock)
	cli.RequestLimit = 1

	if _, err := cli.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Fetch(context.Background()); !errors.Is(err, ErrRequestBudget) {
		t.Fatalf("err=%v, want ErrRequestBudget", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream requests=%d, want 1: the rejected cold read must send nothing", got)
	}
	if got := quotaUsed(cli); got != 1 {
		t.Fatalf("budget slots used=%d, want 1", got)
	}
}

func TestFetch_ExpiredEntryNeedsAQuotaSlot(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(quotaListHandler(&calls, quotaCheck("backup", "backup", strings.Repeat("a", 32))))
	defer server.Close()
	clock := newQuotaClock()
	cli := newQuotaClient(t, server.URL, 30*time.Second, clock)
	cli.RequestLimit = 1

	if _, err := cli.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	clock.Advance(45 * time.Second)
	if _, err := cli.Fetch(context.Background()); !errors.Is(err, ErrRequestBudget) {
		t.Fatalf("err=%v, want ErrRequestBudget for an expired entry without a free slot", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream requests=%d, want 1: an expired entry must not bypass the budget", got)
	}
	cli.RequestLimit = 2
	if _, err := cli.Fetch(context.Background()); err != nil {
		t.Fatalf("expired entry with a free slot: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("upstream requests=%d, want 2 after the expired entry is refetched", got)
	}
}

func TestFetch_CanceledCallerDoesNotConsumeRequestBudget(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(quotaListHandler(&calls, quotaCheck("backup", "backup", strings.Repeat("a", 32))))
	defer server.Close()
	clock := newQuotaClock()
	cli := newQuotaClient(t, server.URL, time.Minute, clock)
	cli.RequestLimit = 1

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cli.Fetch(ctx); err == nil {
		t.Fatal("canceled fetch succeeded")
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("upstream requests=%d, want 0", got)
	}
	if got := quotaUsed(cli); got != 0 {
		t.Fatalf("budget slots used=%d, want 0: a canceled caller sent no request", got)
	}
}

func TestTransitions_WarmCacheDoesNotConsumeRequestBudget(t *testing.T) {
	var listCalls, flipCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/checks/" {
			listCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"checks": []any{
				quotaCheck("one", "one", strings.Repeat("a", 32)),
				quotaCheck("two", "two", strings.Repeat("b", 32)),
			}})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/flips/") {
			flipCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"flips": []any{}})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	clock := newQuotaClock()
	cli := newQuotaClient(t, server.URL, time.Minute, clock)
	cli.RequestLimit = 3
	start := clock.Now().Add(-time.Hour)
	end := clock.Now().Add(-time.Minute)
	filter := Filter{Tags: []string{"ops"}}

	for attempt := 1; attempt <= 2; attempt++ {
		result, err := cli.Transitions(context.Background(), "svc", filter, start, end)
		if err != nil {
			t.Fatalf("transitions %d: %v", attempt, err)
		}
		if result.Requests != 2 || result.Failures != 0 || result.Budget != 0 {
			t.Fatalf("transitions %d result=%+v", attempt, result)
		}
	}
	if got := listCalls.Load(); got != 1 {
		t.Fatalf("list requests=%d, want 1", got)
	}
	if got := flipCalls.Load(); got != 2 {
		t.Fatalf("flips requests=%d, want 2", got)
	}
	if got := quotaUsed(cli); got != 3 {
		t.Fatalf("budget slots used=%d, want 3 for one list and two flips requests", got)
	}
}

func TestTransitions_CoalescedCallersConsumeOneBudgetSlot(t *testing.T) {
	startedList := make(chan struct{})
	releaseListCh := make(chan struct{})
	startedFlips := make(chan struct{})
	releaseFlipsCh := make(chan struct{})
	var listOnce, flipOnce sync.Once
	var releaseListOnce, releaseFlipsOnce sync.Once
	releaseList := func() { releaseListOnce.Do(func() { close(releaseListCh) }) }
	releaseFlips := func() { releaseFlipsOnce.Do(func() { close(releaseFlipsCh) }) }
	var listCalls, flipCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/checks/" {
			listCalls.Add(1)
			listOnce.Do(func() { close(startedList) })
			<-releaseListCh
			_ = json.NewEncoder(w).Encode(map[string]any{"checks": []any{
				quotaCheck("one", "one", strings.Repeat("a", 32)),
			}})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/flips/") {
			flipCalls.Add(1)
			flipOnce.Do(func() { close(startedFlips) })
			<-releaseFlipsCh
			_ = json.NewEncoder(w).Encode(map[string]any{"flips": []any{}})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() {
		releaseList()
		releaseFlips()
	})
	clock := newQuotaClock()
	cli := newQuotaClient(t, server.URL, time.Minute, clock)
	cli.RequestLimit = 2
	start := clock.Now().Add(-time.Hour)
	end := clock.Now().Add(-time.Minute)
	filter := Filter{Tags: []string{"ops"}}

	type outcome struct {
		result TransitionResult
		err    error
	}
	results := make(chan outcome, 2)
	run := func() {
		result, err := cli.Transitions(context.Background(), "svc", filter, start, end)
		results <- outcome{result: result, err: err}
	}
	go run()
	select {
	case <-startedList:
	case <-time.After(5 * time.Second):
		t.Fatal("the first list request did not reach the fixture")
	}
	go run()
	waitForQuotaState(t, "both callers to enter the list cache layer", func() bool {
		return cli.HTTP.Stats().Misses >= 2
	})
	releaseList()
	select {
	case <-startedFlips:
	case <-time.After(5 * time.Second):
		t.Fatal("no flips request reached the fixture: the budget rejected it before admission")
	}
	releaseFlips()

	for index := 0; index < 2; index++ {
		got := <-results
		if got.err != nil {
			t.Fatalf("coalesced transitions %d: %v", index, got.err)
		}
	}
	if got := listCalls.Load(); got != 1 {
		t.Fatalf("list requests=%d, want 1 shared request", got)
	}
	if got := flipCalls.Load(); got != 1 {
		t.Fatalf("flips requests=%d, want 1 shared request", got)
	}
	if got := quotaUsed(cli); got != 2 {
		t.Fatalf("budget slots used=%d, want 2 for one list and one flips request", got)
	}
	if got := cli.HTTP.Stats().Coalesced; got < 1 {
		t.Fatalf("coalesced=%d, want at least one shared wait", got)
	}
}

func TestTransitions_FlipsBudgetRejectionSendsNothing(t *testing.T) {
	var listCalls, flipCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/checks/" {
			listCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"checks": []any{
				quotaCheck("one", "one", strings.Repeat("a", 32)),
				quotaCheck("two", "two", strings.Repeat("b", 32)),
			}})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/flips/") {
			flipCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"flips": []any{}})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	clock := newQuotaClock()
	cli := newQuotaClient(t, server.URL, 0, clock)
	// One list request and one of the two flips requests fit the budget.
	cli.RequestLimit = 2

	result, err := cli.Transitions(context.Background(), "svc", Filter{Tags: []string{"ops"}},
		clock.Now().Add(-time.Hour), clock.Now().Add(-time.Minute))
	if err != nil {
		t.Fatalf("partial evidence must not be an error: %v", err)
	}
	if listCalls.Load() != 1 || flipCalls.Load() != 1 {
		t.Fatalf("list requests=%d flips requests=%d, want 1 each: a rejected flips request sent nothing",
			listCalls.Load(), flipCalls.Load())
	}
	if result.Budget != 1 || result.Requests != 2 || result.Failures != 1 {
		t.Fatalf("result=%+v, want one budget skip and one admitted flips request", result)
	}
	if got := quotaUsed(cli); got != 2 {
		t.Fatalf("budget slots used=%d, want 2", got)
	}
}

func TestFetch_BudgetIsIsolatedPerSource(t *testing.T) {
	var callsA, callsB atomic.Int32
	serverA := httptest.NewServer(quotaListHandler(&callsA, quotaCheck("alpha", "alpha", strings.Repeat("a", 32))))
	defer serverA.Close()
	serverB := httptest.NewServer(quotaListHandler(&callsB, quotaCheck("beta", "beta", strings.Repeat("b", 32))))
	defer serverB.Close()
	clock := newQuotaClock()
	shared := httpx.NewLockedClient(httpx.Options{Timeout: 2 * time.Second, CacheTTL: time.Minute, Now: clock.Now})
	for name, raw := range map[string]string{"a": serverA.URL, "b": serverB.URL} {
		if err := shared.RegisterDestination(name, raw); err != nil {
			t.Fatal(err)
		}
	}
	eng, err := redaction.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	cliA := &Client{HTTP: shared, DestName: "a", Headers: map[string]string{"X-Api-Key": "token-a"}, Redact: eng, Now: clock.Now, RequestLimit: 1}
	cliB := &Client{HTTP: shared, DestName: "b", Headers: map[string]string{"X-Api-Key": "token-b"}, Redact: eng, Now: clock.Now, RequestLimit: 1}

	for attempt := 1; attempt <= 2; attempt++ {
		if _, err := cliA.Fetch(context.Background()); err != nil {
			t.Fatalf("source a fetch %d: %v", attempt, err)
		}
	}
	if _, err := cliB.Fetch(context.Background()); err != nil {
		t.Fatalf("source b fetch: %v", err)
	}
	if got := callsA.Load(); got != 1 {
		t.Fatalf("source a requests=%d, want 1", got)
	}
	if got := callsB.Load(); got != 1 {
		t.Fatalf("source b requests=%d, want 1", got)
	}
}

func TestFetch_CredentialsKeepSeparateCacheAndBudget(t *testing.T) {
	var calls atomic.Int32
	var lastKey atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v3/checks/" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		lastKey.Store(r.Header.Get("X-Api-Key"))
		name := strings.TrimPrefix(r.Header.Get("X-Api-Key"), "token-")
		_ = json.NewEncoder(w).Encode(map[string]any{"checks": []any{
			quotaCheck(name, name, strings.Repeat("a", 32)),
		}})
	}))
	defer server.Close()
	clock := newQuotaClock()
	shared := httpx.NewLockedClient(httpx.Options{Timeout: 2 * time.Second, CacheTTL: time.Minute, Now: clock.Now})
	if err := shared.RegisterDestination("hc", server.URL); err != nil {
		t.Fatal(err)
	}
	eng, err := redaction.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	alpha := &Client{HTTP: shared, DestName: "hc", Headers: map[string]string{"X-Api-Key": "token-alpha"}, Redact: eng, Now: clock.Now, RequestLimit: 1}
	beta := &Client{HTTP: shared, DestName: "hc", Headers: map[string]string{"X-Api-Key": "token-beta"}, Redact: eng, Now: clock.Now, RequestLimit: 1}

	first, err := alpha.Fetch(context.Background())
	if err != nil || len(first.checks) != 1 || first.checks[0].Name != "alpha" {
		t.Fatalf("alpha fetch=%+v err=%v", first, err)
	}
	if _, err := alpha.Fetch(context.Background()); err != nil {
		t.Fatalf("alpha warm cache: %v", err)
	}
	second, err := beta.Fetch(context.Background())
	if err != nil || len(second.checks) != 1 || second.checks[0].Name != "beta" {
		t.Fatalf("beta fetch=%+v err=%v", second, err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("upstream requests=%d, want 2: one per credential", got)
	}
	if got, _ := lastKey.Load().(string); got != "token-beta" {
		t.Fatalf("last upstream key=%q, want the beta credential", got)
	}
}
