package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/audit"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/config"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/evidence"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/httpx"
)

type integration182CapabilitiesView struct {
	SourceCache   httpx.CacheStats    `json:"source_cache"`
	EvidenceCache evidence.CacheStats `json:"evidence_cache"`
	Limits        map[string]any      `json:"limits"`
}

func integration182App(t *testing.T, raw string) *App {
	t.Helper()
	cfg, err := config.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	app, err := NewApp(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), audit.New(io.Discard))
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	return app
}

func integration182Capabilities(t *testing.T, app *App) integration182CapabilitiesView {
	t.Helper()
	server := app.Server()
	t1, t2 := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := server.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = ss.Close() }()
	client := mcp.NewClient(&mcp.Implementation{Name: "integration-182", Version: "1"}, nil)
	session, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = session.Close() }()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "evidence_capabilities", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("evidence_capabilities: %v %+v", err, result)
	}
	if len(result.Content) != 1 {
		t.Fatalf("capabilities content count=%d", len(result.Content))
	}
	content, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content type %T", result.Content[0])
	}
	var out integration182CapabilitiesView
	if err := json.Unmarshal([]byte(content.Text), &out); err != nil {
		t.Fatalf("decode capabilities: %v\n%s", err, content.Text)
	}
	return out
}

func integration182BodyServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func integration182OneSourceConfig(baseURL string, maxBytes int64) string {
	return fmt.Sprintf(`version: 1
limits:
  source_cache_ttl: 5m
  source_cache_max_bytes: %d
  per_source_timeout: 2s
  total_timeout: 10s
sources:
  gatus:
    kind: gatus
    base_url: %s
services: []
`, maxBytes, baseURL)
}

func TestApp_SourceCacheBudgetIsPerApp(t *testing.T) {
	server := integration182BodyServer(t, strings.Repeat("a", 4096))
	raw := integration182OneSourceConfig(server.URL, 65536)
	first := integration182App(t, raw)
	second := integration182App(t, raw)
	if first.SourceCacheBudget == second.SourceCacheBudget {
		t.Fatal("two Apps share one source cache budget")
	}
	resp, _, err := first.HTTP.Get(context.Background(), "gatus", "/status", nil)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}

	firstCaps := integration182Capabilities(t, first)
	if firstCaps.SourceCache.Bytes != 4096 || firstCaps.SourceCache.Size != 1 {
		t.Fatalf("first app source cache=%+v, want 4096 bytes and one entry", firstCaps.SourceCache)
	}
	if firstCaps.SourceCache.MaxBytes != 65536 {
		t.Fatalf("first app max_bytes=%d, want 65536", firstCaps.SourceCache.MaxBytes)
	}
	if got, ok := firstCaps.Limits["source_cache_max_bytes"].(float64); !ok || got != 65536 {
		t.Fatalf("first app limits source_cache_max_bytes=%v", firstCaps.Limits["source_cache_max_bytes"])
	}

	secondCaps := integration182Capabilities(t, second)
	if secondCaps.SourceCache.Bytes != 0 || secondCaps.SourceCache.Size != 0 {
		t.Fatalf("second app saw another App's cache: %+v", secondCaps.SourceCache)
	}
	if secondCaps.SourceCache.MaxBytes != 65536 {
		t.Fatalf("second app max_bytes=%d, want 65536", secondCaps.SourceCache.MaxBytes)
	}
}

func TestApp_SharedBudgetAcrossPlainAndIsolatedClients(t *testing.T) {
	body := strings.Repeat("b", 40000)
	plainServer := integration182BodyServer(t, body)
	isolatedServer := integration182BodyServer(t, body)
	raw := fmt.Sprintf(`version: 1
limits:
  source_cache_ttl: 5m
  source_cache_max_bytes: 65536
  per_source_timeout: 2s
  total_timeout: 10s
sources:
  plain:
    kind: gatus
    base_url: %s
  isolated:
    kind: gatus
    base_url: %s
    allowed_cidrs: ["127.0.0.0/8"]
services: []
`, plainServer.URL, isolatedServer.URL)
	app := integration182App(t, raw)
	if len(app.SourceClients) != 1 {
		t.Fatalf("isolated clients=%d, want 1", len(app.SourceClients))
	}
	beforeEvidence := app.Cache.Stats()
	plainResp, _, err := app.HTTP.Get(context.Background(), "plain", "/status", nil)
	if plainResp != nil {
		_ = plainResp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	isolatedResp, _, err := app.SourceClients[0].Get(context.Background(), "isolated", "/status", nil)
	if isolatedResp != nil {
		_ = isolatedResp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}

	plainStats := app.HTTP.Stats()
	isolatedStats := app.SourceClients[0].Stats()
	if plainStats.MaxBytes != 65536 || isolatedStats.MaxBytes != 65536 {
		t.Fatalf("client max_bytes plain=%d isolated=%d, want 65536 each",
			plainStats.MaxBytes, isolatedStats.MaxBytes)
	}
	if plainStats.Bytes > 65536 || isolatedStats.Bytes > 65536 {
		t.Fatalf("a client exceeds the shared cap: plain=%d isolated=%d", plainStats.Bytes, isolatedStats.Bytes)
	}

	caps := integration182Capabilities(t, app)
	if caps.SourceCache.MaxBytes != 65536 {
		t.Fatalf("capabilities max_bytes=%d, want 65536", caps.SourceCache.MaxBytes)
	}
	if caps.SourceCache.Bytes != 40000 || caps.SourceCache.Size != 1 {
		t.Fatalf("capabilities source cache=%+v, want one 40000 byte entry", caps.SourceCache)
	}
	if got, ok := caps.Limits["source_cache_max_bytes"].(float64); !ok || got != 65536 {
		t.Fatalf("limits source_cache_max_bytes=%v", caps.Limits["source_cache_max_bytes"])
	}

	afterEvidence := app.Cache.Stats()
	if afterEvidence.MaxItems != beforeEvidence.MaxItems || afterEvidence.MaxBytes != beforeEvidence.MaxBytes {
		t.Fatalf("evidence cache limits changed: before=%+v after=%+v", beforeEvidence, afterEvidence)
	}
	if afterEvidence.Items != 0 || afterEvidence.Bytes != 0 {
		t.Fatalf("source reads changed evidence cache usage: %+v", afterEvidence)
	}
	if caps.EvidenceCache.MaxItems != afterEvidence.MaxItems || caps.EvidenceCache.MaxBytes != afterEvidence.MaxBytes {
		t.Fatalf("capabilities evidence cache=%+v, local=%+v", caps.EvidenceCache, afterEvidence)
	}
}

func TestCapabilities_ConcurrentAggregateUsageStaysBounded(t *testing.T) {
	const capBytes int64 = 65536
	server := integration182BodyServer(t, strings.Repeat("c", 4096))
	raw := fmt.Sprintf(`version: 1
limits:
  source_cache_ttl: 5m
  source_cache_max_bytes: %d
  max_tool_calls_per_minute: 1000
  max_concurrent_tools: 64
  per_source_timeout: 2s
  total_timeout: 10s
sources:
  gatus:
    kind: gatus
    base_url: %s
services: []
`, capBytes, server.URL)
	app := integration182App(t, raw)

	var writers sync.WaitGroup
	writeErrors := make(chan error, 128)
	for writer := 0; writer < 4; writer++ {
		writers.Add(1)
		go func(index int) {
			defer writers.Done()
			for round := 0; round < 25; round++ {
				path := fmt.Sprintf("/concurrent-%d-%d", index, round)
				resp, _, err := app.HTTP.Get(context.Background(), "gatus", path, nil)
				if resp != nil {
					_ = resp.Body.Close()
				}
				if err != nil {
					writeErrors <- err
					return
				}
			}
		}(writer)
	}

	capErrors := make(chan string, 8)
	var readers sync.WaitGroup
	for reader := 0; reader < 4; reader++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for round := 0; round < 20; round++ {
				_, out, err := app.toolCapabilities(context.Background(), nil, emptyIn{})
				if err != nil {
					capErrors <- fmt.Sprintf("capabilities error: %v", err)
					return
				}
				caps, ok := out.(CapabilitiesOut)
				if !ok {
					capErrors <- fmt.Sprintf("capabilities type %T", out)
					return
				}
				if caps.SourceCache.MaxBytes != capBytes {
					capErrors <- fmt.Sprintf("max_bytes=%d, want %d", caps.SourceCache.MaxBytes, capBytes)
					return
				}
				if caps.SourceCache.Bytes < 0 || caps.SourceCache.Bytes > capBytes {
					capErrors <- fmt.Sprintf("bytes=%d, cap=%d", caps.SourceCache.Bytes, capBytes)
					return
				}
				if got, ok := caps.Limits["source_cache_max_bytes"].(int64); !ok || got != capBytes {
					capErrors <- fmt.Sprintf("limits source_cache_max_bytes=%v", caps.Limits["source_cache_max_bytes"])
					return
				}
			}
		}()
	}
	readers.Wait()
	writers.Wait()
	close(writeErrors)
	close(capErrors)
	for err := range writeErrors {
		t.Fatalf("concurrent GET: %v", err)
	}
	for failure := range capErrors {
		t.Fatal(failure)
	}

	final := httpx.CombinedStats(append([]*httpx.LockedClient{app.HTTP}, app.SourceClients...)...)
	if final.MaxBytes != capBytes || final.Bytes > capBytes {
		t.Fatalf("final combined stats=%+v, cap=%d", final, capBytes)
	}
}
