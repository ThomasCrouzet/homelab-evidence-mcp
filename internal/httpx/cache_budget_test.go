package httpx

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type cacheBudgetClock struct {
	mu sync.Mutex
	at time.Time
}

func newCacheBudgetClock() *cacheBudgetClock {
	return &cacheBudgetClock{at: time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)}
}

func (c *cacheBudgetClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *cacheBudgetClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

func cacheBudgetBodyServer(t *testing.T, size int) *httptest.Server {
	t.Helper()
	body := strings.Repeat("x", size)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func newCacheBudgetClient(t *testing.T, budget *CacheBudget, server *httptest.Server, clock *cacheBudgetClock, ttl time.Duration) *LockedClient {
	t.Helper()
	opts := Options{Timeout: 2 * time.Second, CacheTTL: ttl, CacheBudget: budget}
	if clock != nil {
		opts.Now = clock.Now
	}
	client := NewLockedClient(opts)
	if err := client.RegisterDestination("src", server.URL); err != nil {
		t.Fatal(err)
	}
	return client
}

// assertCacheBudgetInvariant checks the charged bytes against every client and
// against the cap. The caller must not hold a budget or client lock.
func assertCacheBudgetInvariant(t *testing.T, budget *CacheBudget) {
	t.Helper()
	budget.mu.Lock()
	defer budget.mu.Unlock()
	var sum int64
	for _, client := range budget.clients {
		client.mu.Lock()
		sum += client.cacheBytes
		client.mu.Unlock()
	}
	if budget.used < 0 {
		t.Fatalf("budget used=%d is negative", budget.used)
	}
	if budget.used != sum {
		t.Fatalf("budget used=%d, client bytes=%d", budget.used, sum)
	}
	if budget.used > budget.limit {
		t.Fatalf("budget used=%d exceeds limit %d", budget.used, budget.limit)
	}
}

func cacheBudgetUsed(budget *CacheBudget) int64 {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	return budget.used
}

func cacheBudgetEvictions(budget *CacheBudget) uint64 {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	return budget.evictions
}

func TestCacheBudget_BoundsAggregateBytes(t *testing.T) {
	const limit int64 = 16 << 10
	const bodyBytes = 4 << 10
	budget := NewCacheBudget(limit)
	server := cacheBudgetBodyServer(t, bodyBytes)
	clients := make([]*LockedClient, 0, 8)
	for i := 0; i < 8; i++ {
		clients = append(clients, newCacheBudgetClient(t, budget, server, nil, time.Minute))
	}
	var offered int64
	for round := 0; round < 4; round++ {
		for i, client := range clients {
			path := fmt.Sprintf("/round-%d-client-%d", round, i)
			resp, body, err := client.Get(context.Background(), "src", path, nil)
			if resp != nil {
				_ = resp.Body.Close()
			}
			if err != nil {
				t.Fatalf("client %d round %d: %v", i, round, err)
			}
			offered += int64(len(body))
			if used := cacheBudgetUsed(budget); used > limit {
				t.Fatalf("aggregate bytes=%d exceed cap=%d after %d offered bytes", used, limit, offered)
			}
			assertCacheBudgetInvariant(t, budget)
		}
	}
	if offered < limit*3 {
		t.Fatalf("test offered %d bytes, want at least %d", offered, limit*3)
	}
	stats := CombinedStats(clients...)
	if stats.MaxBytes != limit {
		t.Fatalf("combined max_bytes=%d, want %d", stats.MaxBytes, limit)
	}
	if stats.Bytes != cacheBudgetUsed(budget) {
		t.Fatalf("combined bytes=%d, budget used=%d", stats.Bytes, cacheBudgetUsed(budget))
	}
	if stats.Bytes > limit || stats.Bytes <= 0 {
		t.Fatalf("combined bytes=%d, cap=%d", stats.Bytes, limit)
	}
	if stats.Size == 0 {
		t.Fatal("combined size=0, want a retained entry")
	}
	if got := cacheBudgetEvictions(budget); got == 0 {
		t.Fatal("evictions=0, want cross-client eviction")
	}
}

func TestCacheBudget_EvictsAcrossClients(t *testing.T) {
	const limit int64 = 6000
	budget := NewCacheBudget(limit)
	server := cacheBudgetBodyServer(t, 4000)
	first := newCacheBudgetClient(t, budget, server, nil, time.Minute)
	second := newCacheBudgetClient(t, budget, server, nil, time.Minute)

	resp, _, err := first.Get(context.Background(), "src", "/first", nil)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	resp, _, err = second.Get(context.Background(), "src", "/second", nil)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	first.mu.Lock()
	firstEntries := len(first.cache)
	first.mu.Unlock()
	second.mu.Lock()
	secondEntries := len(second.cache)
	second.mu.Unlock()
	if firstEntries != 0 {
		t.Fatalf("first client kept %d entries, want the global victim removed", firstEntries)
	}
	if secondEntries != 1 {
		t.Fatalf("second client entries=%d, want 1", secondEntries)
	}
	if used := cacheBudgetUsed(budget); used != 4000 {
		t.Fatalf("used=%d, want 4000", used)
	}
	if got := cacheBudgetEvictions(budget); got != 1 {
		t.Fatalf("evictions=%d, want 1", got)
	}
	assertCacheBudgetInvariant(t, budget)
}

func TestCacheBudget_TTLReclaim(t *testing.T) {
	const limit int64 = 8192
	clock := newCacheBudgetClock()
	budget := NewCacheBudget(limit)
	server := cacheBudgetBodyServer(t, 4000)
	first := newCacheBudgetClient(t, budget, server, clock, time.Minute)
	second := newCacheBudgetClient(t, budget, server, clock, time.Minute)

	resp, _, err := first.Get(context.Background(), "src", "/first", nil)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	resp, _, err = second.Get(context.Background(), "src", "/second", nil)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	if used := cacheBudgetUsed(budget); used != 8000 {
		t.Fatalf("used=%d, want 8000", used)
	}
	clock.Advance(2 * time.Minute)
	resp, _, err = second.Get(context.Background(), "src", "/third", nil)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	if used := cacheBudgetUsed(budget); used != 4000 {
		t.Fatalf("used=%d after TTL reclaim, want 4000", used)
	}
	if got := cacheBudgetEvictions(budget); got != 0 {
		t.Fatalf("evictions=%d, want TTL reclaim without eviction", got)
	}
	first.mu.Lock()
	firstEntries := len(first.cache)
	first.mu.Unlock()
	if firstEntries != 0 {
		t.Fatalf("expired entries in another client=%d, want 0", firstEntries)
	}
	assertCacheBudgetInvariant(t, budget)
}

func TestCacheBudget_BodyAboveLimitIsNotStored(t *testing.T) {
	const limit int64 = 1024
	budget := NewCacheBudget(limit)
	server := cacheBudgetBodyServer(t, 2048)
	client := newCacheBudgetClient(t, budget, server, nil, time.Minute)

	resp, body, err := client.Get(context.Background(), "src", "/oversized", nil)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 2048 {
		t.Fatalf("body length=%d, want the full 2048 byte response", len(body))
	}
	if used := cacheBudgetUsed(budget); used != 0 {
		t.Fatalf("used=%d, want no stored bytes", used)
	}
	budget.mu.Lock()
	orderEntries := len(budget.order)
	budget.mu.Unlock()
	if orderEntries != 0 {
		t.Fatalf("order entries=%d, want 0", orderEntries)
	}
	if stats := client.Stats(); stats.Size != 0 || stats.Bytes != 0 || stats.MaxBytes != limit {
		t.Fatalf("stats=%+v", stats)
	}
}

func TestCacheBudget_ConcurrentStress(t *testing.T) {
	const limit int64 = 32 << 10
	const bodyBytes = 1024
	budget := NewCacheBudget(limit)
	server := cacheBudgetBodyServer(t, bodyBytes)
	clients := make([]*LockedClient, 0, 6)
	for i := 0; i < 6; i++ {
		clients = append(clients, newCacheBudgetClient(t, budget, server, nil, time.Minute))
	}

	stop := make(chan struct{})
	statsFailures := make(chan error, 1)
	var statsWG sync.WaitGroup
	statsWG.Add(1)
	go func() {
		defer statsWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			stats := CombinedStats(clients...)
			if stats.MaxBytes != limit {
				select {
				case statsFailures <- fmt.Errorf("combined max_bytes=%d, want %d", stats.MaxBytes, limit):
				default:
				}
				return
			}
			if stats.Bytes < 0 || stats.Bytes > limit {
				select {
				case statsFailures <- fmt.Errorf("combined bytes=%d, cap=%d", stats.Bytes, limit):
				default:
				}
				return
			}
			for _, client := range clients {
				local := client.Stats()
				if local.MaxBytes != limit || local.Bytes > limit || local.Bytes < 0 {
					select {
					case statsFailures <- fmt.Errorf("client stats=%+v, cap=%d", local, limit):
					default:
					}
					return
				}
			}
		}
	}()

	var writers sync.WaitGroup
	errs := make(chan error, 128)
	for i, client := range clients {
		writers.Add(1)
		go func(index int, c *LockedClient) {
			defer writers.Done()
			for round := 0; round < 10; round++ {
				path := fmt.Sprintf("/stress-%d-%d", index, round)
				resp, _, err := c.Get(context.Background(), "src", path, nil)
				if resp != nil {
					_ = resp.Body.Close()
				}
				if err != nil {
					errs <- err
					return
				}
			}
		}(i, client)
	}
	writers.Wait()
	close(stop)
	statsWG.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("stress GET: %v", err)
	}
	select {
	case err := <-statsFailures:
		t.Fatal(err)
	default:
	}

	assertCacheBudgetInvariant(t, budget)
	stats := CombinedStats(clients...)
	if stats.Bytes > limit || stats.MaxBytes != limit {
		t.Fatalf("final combined stats=%+v, cap=%d", stats, limit)
	}
	if got := cacheBudgetEvictions(budget); got == 0 {
		t.Fatal("evictions=0 after offering several times the cap")
	}
}

func TestCacheBudget_NoCrossClientHit(t *testing.T) {
	budget := NewCacheBudget(1 << 20)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"call":%d}`, calls.Add(1))
	}))
	defer server.Close()
	first := newCacheBudgetClient(t, budget, server, nil, time.Minute)
	second := newCacheBudgetClient(t, budget, server, nil, time.Minute)

	firstResp, firstBody, err := first.Get(context.Background(), "src", "/same", nil)
	if firstResp != nil {
		_ = firstResp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	secondResp, secondBody, err := second.Get(context.Background(), "src", "/same", nil)
	if secondResp != nil {
		_ = secondResp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("upstream calls=%d, want one per client", calls.Load())
	}
	if string(firstBody) == string(secondBody) {
		t.Fatalf("clients shared a response body: %s", firstBody)
	}
}

type cacheBudgetCountingContext struct {
	context.Context
	mu      sync.Mutex
	calls   int
	reached chan struct{}
}

func (c *cacheBudgetCountingContext) Done() <-chan struct{} {
	c.mu.Lock()
	c.calls++
	if c.calls == 1 {
		close(c.reached)
	}
	c.mu.Unlock()
	return c.Context.Done()
}

func TestCacheBudget_CoalescingStaysPerClient(t *testing.T) {
	budget := NewCacheBudget(1 << 20)
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		entered <- struct{}{}
		<-release
		_, _ = fmt.Fprintf(w, `{"call":%d}`, n)
	}))
	defer server.Close()
	first := newCacheBudgetClient(t, budget, server, nil, time.Minute)
	second := newCacheBudgetClient(t, budget, server, nil, time.Minute)

	type result struct {
		body []byte
		err  error
	}
	leaders := make(chan result, 2)
	for _, client := range []*LockedClient{first, second} {
		go func(c *LockedClient) {
			resp, body, err := c.Get(context.Background(), "src", "/shared", nil)
			if resp != nil {
				_ = resp.Body.Close()
			}
			leaders <- result{body: body, err: err}
		}(client)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("leaders did not reach the upstream server")
		}
	}

	waiters := make(chan result, 2)
	waiting := []struct {
		client *LockedClient
		ctx    *cacheBudgetCountingContext
	}{
		{first, &cacheBudgetCountingContext{Context: context.Background(), reached: make(chan struct{})}},
		{second, &cacheBudgetCountingContext{Context: context.Background(), reached: make(chan struct{})}},
	}
	for _, pair := range waiting {
		go func(c *LockedClient, ctx context.Context) {
			resp, body, err := c.Get(ctx, "src", "/shared", nil)
			if resp != nil {
				_ = resp.Body.Close()
			}
			waiters <- result{body: body, err: err}
		}(pair.client, pair.ctx)
	}
	// Each waiter closes reached when it blocks on the per-client flight.
	for _, pair := range waiting {
		select {
		case <-pair.ctx.reached:
		case <-time.After(5 * time.Second):
			t.Fatal("waiter did not join a per-client flight")
		}
	}
	close(release)

	leaderBodies := make([][]byte, 0, 2)
	for i := 0; i < 2; i++ {
		out := <-leaders
		if out.err != nil {
			t.Fatalf("leader: %v", out.err)
		}
		leaderBodies = append(leaderBodies, out.body)
	}
	for i := 0; i < 2; i++ {
		out := <-waiters
		if out.err != nil {
			t.Fatalf("waiter: %v", out.err)
		}
		matched := false
		for _, body := range leaderBodies {
			if string(out.body) == string(body) {
				matched = true
			}
		}
		if !matched {
			t.Fatalf("waiter body=%s did not come from a per-client leader", out.body)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("upstream calls=%d, want two per-client attempts", calls.Load())
	}
	if first.Stats().Coalesced != 1 || second.Stats().Coalesced != 1 {
		t.Fatalf("coalesced first=%d second=%d, want 1 each", first.Stats().Coalesced, second.Stats().Coalesced)
	}
}

func TestCacheBudget_PrivateDefaultWhenUnset(t *testing.T) {
	server := cacheBudgetBodyServer(t, 1024)
	first := newCacheBudgetClient(t, nil, server, nil, time.Minute)
	second := newCacheBudgetClient(t, nil, server, nil, time.Minute)
	if first.budget == second.budget {
		t.Fatal("clients without an explicit budget share one budget")
	}
	resp, _, err := first.Get(context.Background(), "src", "/first", nil)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	if used := cacheBudgetUsed(first.budget); used != 1024 {
		t.Fatalf("first used=%d, want 1024", used)
	}
	if used := cacheBudgetUsed(second.budget); used != 0 {
		t.Fatalf("second used=%d, want 0", used)
	}
	stats := CombinedStats(first, second)
	if stats.MaxBytes != 2*DefaultSourceCacheMaxBytes {
		t.Fatalf("combined max_bytes=%d, want %d", stats.MaxBytes, 2*DefaultSourceCacheMaxBytes)
	}
}

func TestCacheBudget_SharedLimitDeduplicated(t *testing.T) {
	const limit int64 = 8192
	budget := NewCacheBudget(limit)
	first := NewLockedClient(Options{CacheTTL: time.Minute, CacheBudget: budget})
	second := NewLockedClient(Options{CacheTTL: time.Minute, CacheBudget: budget})
	if got := CombinedStats(first, second).MaxBytes; got != limit {
		t.Fatalf("combined max_bytes=%d, want one shared cap %d", got, limit)
	}
	private := NewLockedClient(Options{CacheTTL: time.Minute})
	if got := CombinedStats(first, second, private).MaxBytes; got != limit+DefaultSourceCacheMaxBytes {
		t.Fatalf("combined max_bytes=%d, want %d", got, limit+DefaultSourceCacheMaxBytes)
	}
}

func TestCacheBudget_OverwriteAndEntryCap(t *testing.T) {
	budget := NewCacheBudget(1 << 20)
	client := NewLockedClient(Options{CacheTTL: time.Minute, CacheBudget: budget})
	now := time.Now()
	client.cachePutWithTTL("same", http.StatusOK, []byte("aaaa"), now, time.Minute, false)
	client.cachePutWithTTL("same", http.StatusOK, []byte("bb"), now, time.Minute, false)
	if used := cacheBudgetUsed(budget); used != 2 {
		t.Fatalf("used after overwrite=%d, want 2", used)
	}
	budget.mu.Lock()
	orderEntries := len(budget.order)
	budget.mu.Unlock()
	if orderEntries != 1 {
		t.Fatalf("order entries=%d, want 1 after overwrite", orderEntries)
	}

	for i := 0; i < maxCacheEntries+10; i++ {
		client.cachePutWithTTL(fmt.Sprintf("entry-%03d", i), http.StatusOK, []byte("z"), now, time.Minute, false)
	}
	client.mu.Lock()
	entries := len(client.cache)
	client.mu.Unlock()
	if entries != maxCacheEntries {
		t.Fatalf("entries=%d, want local cap %d", entries, maxCacheEntries)
	}
	if used := cacheBudgetUsed(budget); used != maxCacheEntries {
		t.Fatalf("used=%d, want %d", used, maxCacheEntries)
	}
	if got := cacheBudgetEvictions(budget); got != 0 {
		t.Fatalf("evictions=%d, want local entry cap without a global byte eviction", got)
	}
	assertCacheBudgetInvariant(t, budget)
}

func TestCacheBudget_CanceledReadDoesNotStore(t *testing.T) {
	budget := NewCacheBudget(8192)
	server := cacheBudgetBodyServer(t, 1024)
	client := newCacheBudgetClient(t, budget, server, nil, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	resp, _, err := client.Get(ctx, "src", "/canceled", nil)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("canceled read succeeded")
	}
	if used := cacheBudgetUsed(budget); used != 0 {
		t.Fatalf("used=%d after a canceled read, want 0", used)
	}
	budget.mu.Lock()
	orderEntries := len(budget.order)
	budget.mu.Unlock()
	if orderEntries != 0 {
		t.Fatalf("order entries=%d after a canceled read, want 0", orderEntries)
	}
}
