package httpx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type campaignRoundTripper func(*http.Request) (*http.Response, error)

func (f campaignRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type campaignWaiterContext struct {
	context.Context
	mu       sync.Mutex
	calls    int
	first    chan struct{}
	second   chan struct{}
	firstOne sync.Once
	secondOn sync.Once
}

func newCampaignWaiterContext(parent context.Context) *campaignWaiterContext {
	return &campaignWaiterContext{
		Context: parent,
		first:   make(chan struct{}),
		second:  make(chan struct{}),
	}
}

func (c *campaignWaiterContext) Done() <-chan struct{} {
	c.mu.Lock()
	c.calls++
	call := c.calls
	c.mu.Unlock()
	if call == 1 {
		c.firstOne.Do(func() { close(c.first) })
	}
	if call == 2 {
		c.secondOn.Do(func() { close(c.second) })
	}
	return c.Context.Done()
}

func campaignOKResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestGet_CoalescedWaitersSharePersistentFailure(t *testing.T) {
	const waiters = 8
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	client := NewLockedClient(Options{Timeout: 5 * time.Second, CacheTTL: 0})
	if err := client.RegisterDestination("src", "http://source.invalid"); err != nil {
		t.Fatal(err)
	}
	client.client.Transport = campaignRoundTripper(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			once.Do(func() { close(started) })
			<-release
		}
		return nil, errors.New("dial tcp 192.0.2.10:443: connect: connection refused")
	})

	leaderErr := make(chan error, 1)
	go func() {
		_, _, err := client.Get(context.Background(), "src", "/", nil) //nolint:bodyclose
		leaderErr <- err
	}()
	<-started

	waitersCtx := make([]*campaignWaiterContext, waiters)
	errs := make(chan error, waiters)
	for i := 0; i < waiters; i++ {
		waitersCtx[i] = newCampaignWaiterContext(context.Background())
		go func(ctx context.Context) {
			_, _, err := client.Get(ctx, "src", "/", nil) //nolint:bodyclose
			errs <- err
		}(waitersCtx[i])
	}
	for _, ctx := range waitersCtx {
		<-ctx.first
	}
	close(release)

	if err := <-leaderErr; err == nil {
		t.Fatal("leader failure was lost")
	}
	for i := 0; i < waiters; i++ {
		err := <-errs
		if err == nil {
			t.Fatal("waiter received success during a persistent failure")
		}
		if strings.Contains(err.Error(), "192.0.2.10") || strings.Contains(err.Error(), "source.invalid") {
			t.Fatalf("shared error leaked the destination: %v", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream attempts=%d, want 1 shared failure", calls.Load())
	}
}

func TestGet_CoalescedWaitersShareSuccess(t *testing.T) {
	const waiters = 6
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	client := NewLockedClient(Options{Timeout: 5 * time.Second, CacheTTL: 0})
	if err := client.RegisterDestination("src", "http://source.invalid"); err != nil {
		t.Fatal(err)
	}
	client.client.Transport = campaignRoundTripper(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			once.Do(func() { close(started) })
			<-release
		}
		return campaignOKResponse(`{"ok":true}`), nil
	})

	leaderErr := make(chan error, 1)
	go func() {
		_, _, err := client.Get(context.Background(), "src", "/", nil) //nolint:bodyclose
		leaderErr <- err
	}()
	<-started

	waitersCtx := make([]*campaignWaiterContext, waiters)
	bodies := make(chan string, waiters)
	for i := 0; i < waiters; i++ {
		waitersCtx[i] = newCampaignWaiterContext(context.Background())
		go func(ctx context.Context) {
			_, body, err := client.Get(ctx, "src", "/", nil) //nolint:bodyclose
			if err != nil {
				bodies <- "error: " + err.Error()
				return
			}
			bodies <- string(body)
		}(waitersCtx[i])
	}
	for _, ctx := range waitersCtx {
		<-ctx.first
	}
	close(release)

	if err := <-leaderErr; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < waiters; i++ {
		if body := <-bodies; body != `{"ok":true}` {
			t.Fatalf("waiter body=%q", body)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream attempts=%d, want 1 shared success", calls.Load())
	}
	if got := client.Stats().Coalesced; got != waiters {
		t.Fatalf("coalesced=%d, want %d", got, waiters)
	}
}

func TestGet_CanceledLeaderGetsOneReplacementRequest(t *testing.T) {
	const waiters = 4
	type waiterKey struct{}
	var calls atomic.Int32
	var replacementWaiter atomic.Int32
	leaderStarted := make(chan struct{})
	replacementStarted := make(chan struct{})
	replacementRelease := make(chan struct{})
	var leaderOnce, replacementOnce sync.Once
	client := NewLockedClient(Options{Timeout: 5 * time.Second, CacheTTL: 0})
	if err := client.RegisterDestination("src", "http://source.invalid"); err != nil {
		t.Fatal(err)
	}
	client.client.Transport = campaignRoundTripper(func(req *http.Request) (*http.Response, error) {
		switch calls.Add(1) {
		case 1:
			leaderOnce.Do(func() { close(leaderStarted) })
			<-req.Context().Done()
			return nil, req.Context().Err()
		default:
			if index, ok := req.Context().Value(waiterKey{}).(int); ok {
				replacementWaiter.Store(int32(index + 1))
			}
			replacementOnce.Do(func() { close(replacementStarted) })
			<-replacementRelease
			return campaignOKResponse(`{"ok":true}`), nil
		}
	})

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderErr := make(chan error, 1)
	go func() {
		_, _, err := client.Get(leaderCtx, "src", "/", nil) //nolint:bodyclose
		leaderErr <- err
	}()
	<-leaderStarted

	waitersCtx := make([]*campaignWaiterContext, waiters)
	bodies := make(chan string, waiters)
	for i := 0; i < waiters; i++ {
		marker := context.WithValue(context.Background(), waiterKey{}, i)
		waitersCtx[i] = newCampaignWaiterContext(marker)
		go func(ctx context.Context) {
			_, body, err := client.Get(ctx, "src", "/", nil) //nolint:bodyclose
			if err != nil {
				bodies <- "error: " + err.Error()
				return
			}
			bodies <- string(body)
		}(waitersCtx[i])
	}
	for _, ctx := range waitersCtx {
		<-ctx.first
	}
	cancelLeader()
	<-replacementStarted
	replacementIndex := int(replacementWaiter.Load()) - 1
	if replacementIndex < 0 || replacementIndex >= waiters {
		t.Fatalf("replacement waiter=%d is not a tracked waiter", replacementIndex)
	}
	for i, ctx := range waitersCtx {
		if i == replacementIndex {
			continue
		}
		<-ctx.second
	}
	close(replacementRelease)

	if err := <-leaderErr; err == nil {
		t.Fatal("canceled leader returned success")
	}
	for i := 0; i < waiters; i++ {
		if body := <-bodies; body != `{"ok":true}` {
			t.Fatalf("waiter body=%q", body)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("upstream attempts=%d, want one leader and one replacement", calls.Load())
	}
}

func TestGet_CanceledWaiterDoesNotRetry(t *testing.T) {
	var calls atomic.Int32
	leaderStarted := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	client := NewLockedClient(Options{Timeout: 5 * time.Second, CacheTTL: 0})
	if err := client.RegisterDestination("src", "http://source.invalid"); err != nil {
		t.Fatal(err)
	}
	client.client.Transport = campaignRoundTripper(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			once.Do(func() { close(leaderStarted) })
			<-release
		}
		return campaignOKResponse(`{"ok":true}`), nil
	})
	leaderErr := make(chan error, 1)
	go func() {
		_, _, err := client.Get(context.Background(), "src", "/", nil) //nolint:bodyclose
		leaderErr <- err
	}()
	<-leaderStarted

	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	counting := newCampaignWaiterContext(waiterCtx)
	waiterErr := make(chan error, 1)
	go func() {
		_, _, err := client.Get(counting, "src", "/", nil) //nolint:bodyclose
		waiterErr <- err
	}()
	<-counting.first
	cancelWaiter()
	if err := <-waiterErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiter error=%v, want its own cancellation", err)
	}
	close(release)
	if err := <-leaderErr; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("canceled waiter started %d extra requests", calls.Load()-1)
	}
}

func TestGet_DoesNotCoalesceDifferentDestinations(t *testing.T) {
	var calls atomic.Int32
	both := make(chan struct{})
	var once sync.Once
	client := NewLockedClient(Options{Timeout: 5 * time.Second, CacheTTL: 0})
	for _, name := range []string{"one", "two"} {
		if err := client.RegisterDestination(name, "http://source.invalid"); err != nil {
			t.Fatal(err)
		}
	}
	client.client.Transport = campaignRoundTripper(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 2 {
			once.Do(func() { close(both) })
		}
		<-both
		return campaignOKResponse(`{"ok":true}`), nil
	})
	errs := make(chan error, 2)
	for _, name := range []string{"one", "two"} {
		go func(dest string) {
			_, _, err := client.Get(context.Background(), dest, "/", nil) //nolint:bodyclose
			errs <- err
		}(name)
	}
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("different destinations shared a flight: calls=%d", calls.Load())
	}
}
