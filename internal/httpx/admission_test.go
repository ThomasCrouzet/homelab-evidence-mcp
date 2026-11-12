package httpx

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// admitFunc adapts a function to the Admitter seam.
type admitFunc func(context.Context) error

func (f admitFunc) Admit(ctx context.Context) error { return f(ctx) }

// countingAdmitter records how many real requests the client admitted.
type countingAdmitter struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (c *countingAdmitter) Admit(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return c.err
}

func (c *countingAdmitter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

type admissionClock struct {
	mu sync.Mutex
	at time.Time
}

func newAdmissionClock() *admissionClock {
	return &admissionClock{at: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)}
}

func (c *admissionClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *admissionClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

func newAdmissionClient(t *testing.T, server *httptest.Server, clock *admissionClock, cacheTTL time.Duration) *LockedClient {
	t.Helper()
	opts := Options{Timeout: 2 * time.Second, CacheTTL: cacheTTL}
	if clock != nil {
		opts.Now = clock.Now
	}
	client := NewLockedClient(opts)
	if err := client.RegisterDestination("src", server.URL); err != nil {
		t.Fatal(err)
	}
	return client
}

func waitAdmissionSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestGetAdmitted_CacheHitDoesNotConsumeAdmission(t *testing.T) {
	var upstream atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"call":%d}`, upstream.Add(1))
	}))
	defer server.Close()
	clock := newAdmissionClock()
	client := newAdmissionClient(t, server, clock, time.Minute)
	admit := &countingAdmitter{}

	firstResp, first, err := client.GetAdmitted(context.Background(), "src", "/checks/", nil, admit)
	if err != nil {
		t.Fatal(err)
	}
	if firstResp != nil {
		_ = firstResp.Body.Close()
	}
	secondResp, second, err := client.GetAdmitted(context.Background(), "src", "/checks/", nil, admit)
	if err != nil {
		t.Fatal(err)
	}
	if secondResp != nil {
		_ = secondResp.Body.Close()
	}
	if string(first) != string(second) {
		t.Fatalf("second read body=%q, want the cached body %q", second, first)
	}
	if got := upstream.Load(); got != 1 {
		t.Fatalf("upstream requests=%d, want 1", got)
	}
	if got := admit.count(); got != 1 {
		t.Fatalf("admissions=%d, want 1: only the cold read sends a request", got)
	}
	if got := client.Stats().Hits; got != 1 {
		t.Fatalf("cache hits=%d, want 1", got)
	}
}

func TestGetAdmitted_CoalescedWaitersConsumeOneAdmission(t *testing.T) {
	const waiters = 9
	var upstream atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var releaseOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if upstream.Add(1) == 1 {
			once.Do(func() { close(started) })
			<-release
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	clock := newAdmissionClock()
	client := newAdmissionClient(t, server, clock, time.Minute)
	admit := &countingAdmitter{}

	leaderErr := make(chan error, 1)
	go func() {
		resp, _, err := client.GetAdmitted(context.Background(), "src", "/checks/", nil, admit)
		if resp != nil {
			_ = resp.Body.Close()
		}
		leaderErr <- err
	}()
	waitAdmissionSignal(t, started, "the leader to send the upstream request")

	waitersCtx := make([]*campaignWaiterContext, waiters)
	errs := make(chan error, waiters)
	for index := 0; index < waiters; index++ {
		waitersCtx[index] = newCampaignWaiterContext(context.Background())
		go func(ctx context.Context) {
			resp, _, err := client.GetAdmitted(ctx, "src", "/checks/", nil, admit)
			if resp != nil {
				_ = resp.Body.Close()
			}
			errs <- err
		}(waitersCtx[index])
	}
	for index, ctx := range waitersCtx {
		waitAdmissionSignal(t, ctx.first, fmt.Sprintf("waiter %d to join the flight", index))
	}
	releaseOnce.Do(func() { close(release) })

	if err := <-leaderErr; err != nil {
		t.Fatalf("leader: %v", err)
	}
	for index := 0; index < waiters; index++ {
		if err := <-errs; err != nil {
			t.Fatalf("waiter %d: %v", index, err)
		}
	}
	if got := upstream.Load(); got != 1 {
		t.Fatalf("upstream requests=%d, want 1", got)
	}
	if got := admit.count(); got != 1 {
		t.Fatalf("admissions=%d, want 1 for one coalesced request", got)
	}
	if got := client.Stats().Coalesced; got != waiters {
		t.Fatalf("coalesced=%d, want %d", got, waiters)
	}
}

func TestGetAdmitted_RejectionSendsNoUpstreamRequest(t *testing.T) {
	var upstream atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream.Add(1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	clock := newAdmissionClock()
	client := newAdmissionClient(t, server, clock, time.Minute)
	errBudget := errors.New("campaign budget exhausted")
	admit := &countingAdmitter{err: errBudget}

	for attempt := 1; attempt <= 2; attempt++ {
		resp, body, err := client.GetAdmitted(context.Background(), "src", "/checks/", nil, admit)
		if resp != nil {
			_ = resp.Body.Close()
		}
		if !errors.Is(err, errBudget) {
			t.Fatalf("attempt %d err=%v, want the admission error", attempt, err)
		}
		if resp != nil || body != nil {
			t.Fatalf("attempt %d returned a response for a rejected request", attempt)
		}
	}
	if got := upstream.Load(); got != 0 {
		t.Fatalf("upstream requests=%d, want 0", got)
	}
	if got := admit.count(); got != 2 {
		t.Fatalf("admissions=%d, want 2 cold attempts", got)
	}
	if got := client.Stats().Hits; got != 0 {
		t.Fatalf("cache hits=%d, want 0", got)
	}
}

func TestGetAdmitted_RejectionIsSharedWithCoalescedWaiters(t *testing.T) {
	const waiters = 7
	var upstream atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream.Add(1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	clock := newAdmissionClock()
	client := newAdmissionClient(t, server, clock, time.Minute)
	errBudget := errors.New("campaign budget exhausted")
	var admitted atomic.Int32
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	admit := admitFunc(func(context.Context) error {
		if admitted.Add(1) == 1 {
			entered <- struct{}{}
		}
		<-release
		return errBudget
	})

	leaderErr := make(chan error, 1)
	go func() {
		resp, _, err := client.GetAdmitted(context.Background(), "src", "/checks/", nil, admit)
		if resp != nil {
			_ = resp.Body.Close()
		}
		leaderErr <- err
	}()
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	waitAdmissionSignal(t, entered, "the leader to ask for admission")

	waitersCtx := make([]*campaignWaiterContext, waiters)
	errs := make(chan error, waiters)
	for index := 0; index < waiters; index++ {
		waitersCtx[index] = newCampaignWaiterContext(context.Background())
		go func(ctx context.Context) {
			resp, _, err := client.GetAdmitted(ctx, "src", "/checks/", nil, admit)
			if resp != nil {
				_ = resp.Body.Close()
			}
			errs <- err
		}(waitersCtx[index])
	}
	for index, ctx := range waitersCtx {
		waitAdmissionSignal(t, ctx.first, fmt.Sprintf("waiter %d to join the flight", index))
	}
	releaseOnce.Do(func() { close(release) })

	if err := <-leaderErr; !errors.Is(err, errBudget) {
		t.Fatalf("leader err=%v, want the shared admission error", err)
	}
	for index := 0; index < waiters; index++ {
		if err := <-errs; !errors.Is(err, errBudget) {
			t.Fatalf("waiter %d err=%v, want the shared admission error", index, err)
		}
	}
	if got := admitted.Load(); got != 1 {
		t.Fatalf("admissions=%d, want 1 shared rejection", got)
	}
	if got := upstream.Load(); got != 0 {
		t.Fatalf("upstream requests=%d, want 0", got)
	}
}

func TestGetAdmitted_CanceledCallerSkipsAdmission(t *testing.T) {
	var upstream atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream.Add(1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	clock := newAdmissionClock()
	client := newAdmissionClient(t, server, clock, time.Minute)
	admit := &countingAdmitter{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	resp, _, err := client.GetAdmitted(ctx, "src", "/checks/", nil, admit)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context.Canceled", err)
	}
	if got := admit.count(); got != 0 {
		t.Fatalf("admissions=%d, want 0 for a canceled caller", got)
	}
	if got := upstream.Load(); got != 0 {
		t.Fatalf("upstream requests=%d, want 0", got)
	}
}

func TestGetAdmitted_ExpiredEntryRequiresAdmission(t *testing.T) {
	var upstream atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"call":%d}`, upstream.Add(1))
	}))
	defer server.Close()
	clock := newAdmissionClock()
	client := newAdmissionClient(t, server, clock, time.Minute)
	admit := &countingAdmitter{}

	firstResp, first, err := client.GetAdmitted(context.Background(), "src", "/checks/", nil, admit)
	if err != nil {
		t.Fatal(err)
	}
	if firstResp != nil {
		_ = firstResp.Body.Close()
	}
	clock.Advance(2 * time.Minute)
	secondResp, second, err := client.GetAdmitted(context.Background(), "src", "/checks/", nil, admit)
	if err != nil {
		t.Fatal(err)
	}
	if secondResp != nil {
		_ = secondResp.Body.Close()
	}
	if string(first) == string(second) {
		t.Fatalf("expired entry served the stored body: %q", second)
	}
	if got := upstream.Load(); got != 2 {
		t.Fatalf("upstream requests=%d, want 2", got)
	}
	if got := admit.count(); got != 2 {
		t.Fatalf("admissions=%d, want 2 for two upstream requests", got)
	}
}

func TestGetAdmitted_DifferentCredentialsAreAdmittedSeparately(t *testing.T) {
	var upstream atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream.Add(1)
		_, _ = fmt.Fprintf(w, `{"key":%q}`, r.Header.Get("X-Api-Key"))
	}))
	defer server.Close()
	clock := newAdmissionClock()
	client := newAdmissionClient(t, server, clock, time.Minute)
	admit := &countingAdmitter{}

	alpha := map[string]string{"X-Api-Key": "alpha"}
	beta := map[string]string{"X-Api-Key": "beta"}
	firstResp, first, err := client.GetAdmitted(context.Background(), "src", "/checks/", alpha, admit)
	if err != nil {
		t.Fatal(err)
	}
	if firstResp != nil {
		_ = firstResp.Body.Close()
	}
	cachedResp, cached, err := client.GetAdmitted(context.Background(), "src", "/checks/", alpha, admit)
	if err != nil {
		t.Fatal(err)
	}
	if cachedResp != nil {
		_ = cachedResp.Body.Close()
	}
	secondResp, second, err := client.GetAdmitted(context.Background(), "src", "/checks/", beta, admit)
	if err != nil {
		t.Fatal(err)
	}
	if secondResp != nil {
		_ = secondResp.Body.Close()
	}
	if string(first) != string(cached) {
		t.Fatalf("warm credential read body=%q, want %q", cached, first)
	}
	if string(first) == string(second) {
		t.Fatalf("a second credential reused the first credential's cached body: %q", second)
	}
	if got := upstream.Load(); got != 2 {
		t.Fatalf("upstream requests=%d, want 2", got)
	}
	if got := admit.count(); got != 2 {
		t.Fatalf("admissions=%d, want 2: one per credential", got)
	}
}

// The admission policy is scoped to the destination and its credentials. A
// rejection for one destination must not block another destination on the same
// locked client, and each policy is asked only for its own network attempts.
func TestGetAdmitted_DestinationPoliciesAreIsolated(t *testing.T) {
	const bodyA = `{"destination":"a"}`
	var callsA, callsB atomic.Int32
	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callsA.Add(1)
		_, _ = w.Write([]byte(bodyA))
	}))
	defer serverA.Close()
	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callsB.Add(1)
		_, _ = w.Write([]byte(`{"destination":"b"}`))
	}))
	defer serverB.Close()
	clock := newAdmissionClock()
	client := NewLockedClient(Options{Timeout: 2 * time.Second, CacheTTL: time.Minute, Now: clock.Now})
	for name, raw := range map[string]string{"a": serverA.URL, "b": serverB.URL} {
		if err := client.RegisterDestination(name, raw); err != nil {
			t.Fatal(err)
		}
	}
	admitA := &countingAdmitter{}
	errB := errors.New("destination b policy rejected the request")
	admitB := &countingAdmitter{err: errB}

	resp, body, err := client.GetAdmitted(context.Background(), "a", "/checks/", nil, admitA)
	if err != nil {
		t.Fatal(err)
	}
	if resp != nil {
		_ = resp.Body.Close()
	}
	if string(body) != bodyA {
		t.Fatalf("destination a body=%q", body)
	}

	rejectedResp, rejectedBody, err := client.GetAdmitted(context.Background(), "b", "/checks/", nil, admitB)
	if rejectedResp != nil {
		_ = rejectedResp.Body.Close()
	}
	if !errors.Is(err, errB) {
		t.Fatalf("destination b err=%v, want its own rejection", err)
	}
	if rejectedBody != nil {
		t.Fatal("destination b returned a body for a rejected request")
	}

	// A second destination must still admit after the other destination rejects.
	resp, body, err = client.GetAdmitted(context.Background(), "a", "/other/", nil, admitA)
	if err != nil {
		t.Fatalf("destination a after destination b rejection: %v", err)
	}
	if resp != nil {
		_ = resp.Body.Close()
	}
	if string(body) != bodyA {
		t.Fatalf("destination a second body=%q", body)
	}
	if got := callsA.Load(); got != 2 {
		t.Fatalf("destination a requests=%d, want 2", got)
	}
	if got := callsB.Load(); got != 0 {
		t.Fatalf("destination b requests=%d, want 0", got)
	}
	if got := admitA.count(); got != 2 {
		t.Fatalf("destination a admissions=%d, want 2", got)
	}
	if got := admitB.count(); got != 1 {
		t.Fatalf("destination b admissions=%d, want 1", got)
	}
}
