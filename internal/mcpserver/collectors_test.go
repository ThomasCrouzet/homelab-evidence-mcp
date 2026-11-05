package mcpserver

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/config"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/evidence"
)

func TestFilterCurrentSnapshots(t *testing.T) {
	end := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	start := end.Add(-time.Hour)
	items := []evidence.Item{
		{SourceID: "old", ObservedAt: start.Add(-time.Second)},
		{SourceID: "inside", ObservedAt: end.Add(-time.Minute)},
		{SourceID: "request-delay", ObservedAt: end.Add(20 * time.Second)},
		{SourceID: "future", ObservedAt: end.Add(31 * time.Second)},
	}
	got := filterCurrentSnapshots(items, start, end)
	if len(got) != 2 || got[0].SourceID != "inside" || got[1].SourceID != "request-delay" {
		t.Fatalf("got=%+v", got)
	}
	for _, item := range got {
		if item.WindowStart == nil || item.WindowEnd == nil {
			t.Fatalf("window missing: %+v", item)
		}
	}
}

func TestSourceErrorOutcomeDistinguishesCancellation(t *testing.T) {
	app := &App{}
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	outcome := app.sourceErrorOutcome(canceledCtx, canceledCtx, "source", evidence.SourceGatus, context.Canceled, time.Now())
	if outcome.Status != "canceled" {
		t.Fatalf("canceled=%+v", outcome)
	}

	deadlineCtx, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	outcome = app.sourceErrorOutcome(deadlineCtx, deadlineCtx, "source", evidence.SourceGatus, context.DeadlineExceeded, time.Now())
	if outcome.Status != "timeout" {
		t.Fatalf("deadline=%+v", outcome)
	}

	outcome = app.sourceErrorOutcome(context.Background(), context.Background(), "source", evidence.SourceGatus, context.Canceled, time.Now())
	if outcome.Status != "error" {
		t.Fatalf("plain error=%+v", outcome)
	}
}

func TestWithSourceTimeoutSkipsCanceledWork(t *testing.T) {
	app := &App{
		Cfg:       &config.Config{Limits: config.Limits{PerSourceTimeout: time.Second}},
		sourceSem: make(chan struct{}, 1),
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	outcome, items := app.withSourceTimeout(ctx, "source", evidence.SourceGatus, func(context.Context) ([]evidence.Item, error) {
		called = true
		return nil, nil
	})
	if called || len(items) != 0 || outcome.Status != "canceled" {
		t.Fatalf("called=%v items=%d outcome=%+v", called, len(items), outcome)
	}
}

func TestWithSourceTimeoutLimitsConcurrency(t *testing.T) {
	app := &App{
		Cfg: &config.Config{Limits: config.Limits{
			PerSourceTimeout: time.Second,
		}},
		sourceSem: make(chan struct{}, 2),
	}

	var current atomic.Int64
	var maximum atomic.Int64
	var wg sync.WaitGroup
	outcomes := make(chan evidence.SourceOutcome, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			outcome, _ := app.withSourceTimeout(
				context.Background(),
				"source",
				evidence.SourceHealthchecks,
				func(context.Context) ([]evidence.Item, error) {
					active := current.Add(1)
					for {
						previous := maximum.Load()
						if active <= previous || maximum.CompareAndSwap(previous, active) {
							break
						}
					}
					time.Sleep(20 * time.Millisecond)
					current.Add(-1)
					return nil, nil
				},
			)
			outcomes <- outcome
		}()
	}
	wg.Wait()
	close(outcomes)

	if got := maximum.Load(); got > 2 {
		t.Fatalf("max concurrency=%d, want <= 2", got)
	}
	for outcome := range outcomes {
		if outcome.Status != "ok" {
			t.Fatalf("unexpected outcome: %+v", outcome)
		}
	}
}
