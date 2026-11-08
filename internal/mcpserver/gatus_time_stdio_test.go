package mcpserver

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// This test detects undated Gatus results that look recent and undated history
// that acquires a past timestamp.
func TestStdioGatusUnknownObservationTime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var undatedCalls atomic.Int32
	undated := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/endpoints/statuses" {
			http.NotFound(w, r)
			return
		}
		undatedCalls.Add(1)
		_, _ = w.Write([]byte(`[{"name":"api","key":"api","results":[{"success":true,"status":200,"timestamp":"invalid"}]}]`))
	}))
	defer undated.Close()

	validTime := time.Now().UTC().Truncate(time.Second).Add(-5 * time.Minute)
	mixed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/endpoints/statuses" {
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprintf(w, `[{"name":"api","key":"api","results":[
			{"success":true,"status":200,"timestamp":"invalid"},
			{"success":false,"status":503,"timestamp":%q},
			{"success":true,"status":200}
		]}]`, validTime.Format(time.RFC3339))
	}))
	defer mixed.Close()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configText := fmt.Sprintf(`version: 1
limits:
  default_window: 15m
  max_incident_window: 1h
  source_cache_ttl: 1m
sources:
  gatus-undated:
    kind: gatus
    base_url: %s
  gatus-mixed:
    kind: gatus
    base_url: %s
services:
  - id: undated
    sources:
      gatus: { source: gatus-undated, endpoint_key: api }
  - id: mixed
    sources:
      gatus: { source: gatus-mixed, endpoint_key: api }
`, undated.URL, mixed.URL)
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}

	binary := stdioTestBinary(t, ctx)
	var stderr bytes.Buffer
	command := exec.CommandContext(ctx, binary, "--config", configPath)
	command.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "gatus-time-fixture", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command, TerminateDuration: stdioShutdownTimeout}, nil)
	if err != nil {
		t.Fatalf("connect to executable: %v\nstderr:\n%s", err, stderr.String())
	}
	defer func() { _ = session.Close() }()

	type statusBody struct {
		Items []struct {
			ID         string         `json:"id"`
			ObservedAt time.Time      `json:"observed_at"`
			Freshness  string         `json:"freshness"`
			Attributes map[string]any `json:"attributes"`
			Source     string         `json:"source"`
		} `json:"items"`
	}
	callUndated := func() statusBody {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name: "service_status", Arguments: map[string]any{"service_id": "undated"},
		})
		if err != nil || result.IsError {
			t.Fatalf("undated service_status failed: %v", err)
		}
		var body statusBody
		executableE2EDecode(t, mustJSON(t, result.StructuredContent), &body)
		if len(body.Items) != 1 {
			t.Fatalf("items=%d: %s", len(body.Items), mustJSON(t, result.StructuredContent))
		}
		item := body.Items[0]
		if item.Freshness != "unknown" || item.Attributes["source_time_known"] != false ||
			item.Attributes["collection_time_derived"] != true {
			t.Fatalf("undated result looked dated: %+v", item)
		}
		return body
	}
	first := callUndated()
	callUndated()
	if undatedCalls.Load() != 1 {
		t.Fatalf("warm source cache used %d upstream requests", undatedCalls.Load())
	}

	cachedResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "get_evidence", Arguments: map[string]any{"id": first.Items[0].ID},
	})
	if err != nil || cachedResult.IsError {
		t.Fatalf("get_evidence failed: %v", err)
	}
	var cachedBody struct {
		Item struct {
			Freshness       string     `json:"freshness"`
			SourceFreshness string     `json:"source_freshness"`
			CacheReadAt     *time.Time `json:"cache_read_at"`
		} `json:"item"`
	}
	executableE2EDecode(t, mustJSON(t, cachedResult.StructuredContent), &cachedBody)
	if cachedBody.Item.Freshness != "cached" || cachedBody.Item.SourceFreshness != "unknown" ||
		cachedBody.Item.CacheReadAt == nil {
		t.Fatalf("cache delivery lost the source freshness: %+v", cachedBody.Item)
	}

	historical, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "incident_context", Arguments: map[string]any{
			"service_id": "undated",
			"start":      time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339),
			"end":        time.Now().UTC().Add(-time.Hour).Format(time.RFC3339),
		},
	})
	if err != nil || historical.IsError {
		t.Fatalf("historical incident_context failed: %v", err)
	}
	var historicalBody statusBody
	executableE2EDecode(t, mustJSON(t, historical.StructuredContent), &historicalBody)
	for _, item := range historicalBody.Items {
		if item.Source == "gatus" {
			t.Fatalf("undated result invented past history: %+v", item)
		}
	}

	mixedResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "service_status", Arguments: map[string]any{"service_id": "mixed"},
	})
	if err != nil || mixedResult.IsError {
		t.Fatalf("mixed service_status failed: %v", err)
	}
	var mixedBody statusBody
	executableE2EDecode(t, mustJSON(t, mixedResult.StructuredContent), &mixedBody)
	if len(mixedBody.Items) != 1 || mixedBody.Items[0].Attributes["http_status"] != float64(503) ||
		mixedBody.Items[0].Attributes["source_time_known"] != true ||
		mixedBody.Items[0].Freshness == "unknown" {
		t.Fatalf("mixed timestamps did not select the latest valid result: %+v", mixedBody.Items)
	}
}
