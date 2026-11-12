package mcpserver

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/audit"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/config"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/evidence"
)

// runtimeChecksPayload gives one current check for a fixture source.
func runtimeChecksPayload(state string) string {
	return fmt.Sprintf(`{"checks":[{"name":"runtime-cron","slug":"runtime-cron","tags":"shared","unique_key":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",%s}]}`,
		state)
}

// This test detects a shared Healthchecks inventory that waits for the overall
// tool deadline instead of the per-source deadline, and a stalled source that
// removes the evidence of a fast source.
func TestStdioSharedHealthchecksInventoryUsesPerSourceDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	t.Setenv("HC_RUNTIME_TOKEN", "runtime-token")

	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/api/v3/checks/") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(runtimeChecksPayload(`"status":"down","grace":60,"last_ping":"` +
			time.Now().UTC().Add(-time.Minute).Format(time.RFC3339) + `"`)))
	}))
	defer fast.Close()
	// The stalled source keeps its connection open until the caller cancels it.
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer stalled.Close()

	raw := fmt.Sprintf(`version: 1
limits:
  per_source_timeout: 400ms
  total_timeout: 6s
  max_cron_window: 24h
  source_cache_ttl: 0s
sources:
  hc-fast:
    kind: healthchecks
    base_url: %s
    token_env: HC_RUNTIME_TOKEN
  hc-stalled:
    kind: healthchecks
    base_url: %s
    token_env: HC_RUNTIME_TOKEN
services:
  - id: fast-cron
    sources:
      healthchecks: { source: hc-fast, check_tags: [shared] }
  - id: stalled-cron
    sources:
      healthchecks: { source: hc-stalled, check_tags: [shared] }
`, fast.URL, stalled.URL)
	binary := stdioTestBinary(t, ctx)
	session := runtimeConnect(t, ctx, binary, runtimeWriteConfig(t, raw))

	started := time.Now()
	callCtx, stopCall := context.WithTimeout(ctx, 10*time.Second)
	defer stopCall()
	rawResult := executableE2ECall(t, callCtx, session, "failed_crons", map[string]any{"duration": "1h"})
	elapsed := time.Since(started)
	if elapsed >= 3*time.Second {
		t.Fatalf("global failed_crons waited %s for a stalled source, per-source deadline is 400ms", elapsed)
	}
	sources := runtimeSources(t, rawResult)
	fastOutcome := runtimeFindSource(t, sources, "hc-fast", evidence.SourceHealthchecks)
	if fastOutcome.Status != "ok" || !fastOutcome.Complete {
		t.Fatalf("fast source outcome=%+v", fastOutcome)
	}
	stalledOutcome := runtimeFindSource(t, sources, "hc-stalled", evidence.SourceHealthchecks)
	if stalledOutcome.Status != "timeout" || stalledOutcome.Reason != "timeout" || stalledOutcome.Complete {
		t.Fatalf("stalled source outcome=%+v", stalledOutcome)
	}
	var parsed struct {
		Items []evidence.Item `json:"items"`
	}
	executableE2EDecode(t, rawResult, &parsed)
	found := false
	for _, item := range parsed.Items {
		if item.ServiceID == "fast-cron" {
			found = true
		}
	}
	if !found {
		t.Fatalf("fast source evidence is missing: %s", rawResult)
	}
}

// This test detects shared inventories that bypass the global source
// semaphore while other tools collect sources at the same time.
func TestStdioSharedHealthchecksInventoryUsesGlobalSourceSlots(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	t.Setenv("HC_RUNTIME_TOKEN", "runtime-token")

	var inFlight, maxInFlight, requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		current := inFlight.Add(1)
		for {
			observed := maxInFlight.Load()
			if current <= observed || maxInFlight.CompareAndSwap(observed, current) {
				break
			}
		}
		defer inFlight.Add(-1)
		select {
		case <-time.After(200 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/hc-"):
			_, _ = w.Write([]byte(runtimeChecksPayload(`"status":"down","grace":60,"last_ping":"` +
				time.Now().UTC().Add(-time.Minute).Format(time.RFC3339) + `"`)))
		default:
			_, _ = w.Write([]byte(`[{"name":"runtime","key":"runtime","results":[{"success":true,"status":200,"timestamp":"` +
				time.Now().UTC().Add(-time.Minute).Format(time.RFC3339) + `"}]}]`))
		}
	}))
	defer server.Close()

	const (
		gatusSources        = 6
		healthchecksSources = 5
	)
	var config strings.Builder
	config.WriteString(`version: 1
limits:
  per_source_timeout: 2s
  total_timeout: 20s
  max_cron_window: 24h
  max_concurrent_tools: 8
  source_cache_ttl: 0s
sources:
`)
	for index := 1; index <= gatusSources; index++ {
		fmt.Fprintf(&config, `  gatus-%d:
    kind: gatus
    base_url: %s/g-%d
`, index, server.URL, index)
	}
	for index := 1; index <= healthchecksSources; index++ {
		fmt.Fprintf(&config, `  hc-%d:
    kind: healthchecks
    base_url: %s/hc-%d
    token_env: HC_RUNTIME_TOKEN
`, index, server.URL, index)
	}
	config.WriteString("services:\n")
	for index := 1; index <= gatusSources; index++ {
		fmt.Fprintf(&config, `  - id: gatus-svc-%d
    sources:
      gatus: { source: gatus-%d, endpoint_key: runtime }
`, index, index)
	}
	for index := 1; index <= healthchecksSources; index++ {
		fmt.Fprintf(&config, `  - id: hc-svc-%d
    sources:
      healthchecks: { source: hc-%d, check_tags: [shared] }
`, index, index)
	}

	binary := stdioTestBinary(t, ctx)
	session := runtimeConnect(t, ctx, binary, runtimeWriteConfig(t, config.String()))

	var wg sync.WaitGroup
	errs := make(chan error, gatusSources+1)
	// Concurrent incident_context calls collect one Gatus source each.
	for index := 1; index <= gatusSources; index++ {
		wg.Add(1)
		go func(serviceIndex int) {
			defer wg.Done()
			callCtx, stop := context.WithTimeout(ctx, 20*time.Second)
			defer stop()
			result, err := session.CallTool(callCtx, &mcp.CallToolParams{
				Name:      "incident_context",
				Arguments: map[string]any{"service_id": fmt.Sprintf("gatus-svc-%d", serviceIndex), "duration": "30m"},
			})
			if err == nil && result.IsError {
				err = fmt.Errorf("incident_context gatus-svc-%d returned an error result", serviceIndex)
			}
			if err != nil {
				errs <- err
			}
		}(index)
	}
	// The global failed_crons call fetches one inventory for every Healthchecks source.
	wg.Add(1)
	go func() {
		defer wg.Done()
		callCtx, stop := context.WithTimeout(ctx, 20*time.Second)
		defer stop()
		result, err := session.CallTool(callCtx, &mcp.CallToolParams{
			Name:      "failed_crons",
			Arguments: map[string]any{"duration": "1h"},
		})
		if err == nil && result.IsError {
			err = fmt.Errorf("failed_crons returned an error result")
		}
		if err != nil {
			errs <- err
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent tool call failed: %v", err)
	}

	observed := int(maxInFlight.Load())
	if observed > maxConcurrentSourceRequests {
		t.Fatalf("%d concurrent source collections exceeded the limit of %d", observed, maxConcurrentSourceRequests)
	}
	if requests.Load() < gatusSources+healthchecksSources {
		t.Fatalf("only %d source requests were made", requests.Load())
	}
}

// This test detects a canceled shared inventory that keeps a global source slot.
func TestSharedHealthchecksInventoryReleasesSourceSlotOnCancel(t *testing.T) {
	t.Setenv("HC_RUNTIME_TOKEN", "runtime-token")
	var block atomic.Bool
	block.Store(true)
	started := make(chan struct{}, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/api/v3/checks/") {
			http.NotFound(w, r)
			return
		}
		if block.Load() {
			started <- struct{}{}
			<-r.Context().Done()
			return
		}
		_, _ = w.Write([]byte(runtimeChecksPayload(`"status":"down","grace":60,"last_ping":"` +
			time.Now().UTC().Add(-time.Minute).Format(time.RFC3339) + `"`)))
	}))
	defer server.Close()

	raw := fmt.Sprintf(`version: 1
limits:
  per_source_timeout: 5s
  total_timeout: 10s
  max_cron_window: 24h
  source_cache_ttl: 0s
sources:
  hc-cancel:
    kind: healthchecks
    base_url: %s
    token_env: HC_RUNTIME_TOKEN
services:
  - id: cancel-cron
    sources:
      healthchecks: { source: hc-cancel, check_tags: [shared] }
`, server.URL)
	cfg, err := config.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	app, err := NewApp(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), audit.New(io.Discard))
	if err != nil {
		t.Fatal(err)
	}

	callCtx, stopCall := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = app.collectRegisteredFailedCrons(callCtx, time.Now().UTC().Add(-time.Hour), time.Now().UTC())
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the shared inventory request did not start")
	}
	stopCall()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the canceled shared inventory did not return")
	}
	if held := len(app.sourceSem); held != 0 {
		t.Fatalf("%d source slots stayed acquired after cancellation", held)
	}

	block.Store(false)
	outcomes, _, _ := app.collectRegisteredFailedCrons(context.Background(), time.Now().UTC().Add(-time.Hour), time.Now().UTC())
	if len(outcomes) != 1 || outcomes[0].Status != "ok" {
		t.Fatalf("a collection after cancellation failed: %+v", outcomes)
	}
}
