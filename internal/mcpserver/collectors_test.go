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
			t.Fatalf("fenêtre absente : %+v", item)
		}
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
		t.Fatalf("concurrence maximale=%d, attendu <= 2", got)
	}
	for outcome := range outcomes {
		if outcome.Status != "ok" {
			t.Fatalf("résultat inattendu : %+v", outcome)
		}
	}
}
