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
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// This test detects a missing endpoint that looks like complete empty history,
// malformed Gatus records that disappear without a count, and a bounded Gatus
// result list that looks like full window coverage. Fixtures are synthetic.
func TestStdioGatusHistoryCoverage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	now := time.Now().UTC().Truncate(time.Second)
	valid := now.Add(-40 * time.Minute).Format(time.RFC3339)
	invalidSuccess := now.Add(-35 * time.Minute).Format(time.RFC3339)
	explicitFailure := now.Add(-30 * time.Minute).Format(time.RFC3339)

	gatus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/endpoints/statuses" {
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprintf(w, `[
			{"name":"api","key":"api","results":[
				{"success":true,"status":200,"timestamp":%q},
				{"status":500,"timestamp":%q},
				{"success":false,"status":500,"timestamp":"invalid"},
				{"success":false,"timestamp":%q}
			]},
			{"name":"empty","key":"empty","results":[]},
			{"name":"incomplete","key":"incomplete","results":[
				{"status":500,"timestamp":%q}
			]}
		]`, valid, invalidSuccess, explicitFailure, valid)
	}))
	defer gatus.Close()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configText := fmt.Sprintf(`version: 1
limits:
  default_window: 1h
  max_incident_window: 2h
sources:
  gatus:
    kind: gatus
    base_url: %s
services:
  - id: history
    sources:
      gatus: { source: gatus, endpoint_key: api }
  - id: missing
    sources:
      gatus: { source: gatus, endpoint_key: absent }
  - id: empty
    sources:
      gatus: { source: gatus, endpoint_key: empty }
  - id: incomplete
    sources:
      gatus: { source: gatus, endpoint_key: incomplete }
`, gatus.URL)
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}

	binary := stdioTestBinary(t, ctx)
	var stderr bytes.Buffer
	command := exec.CommandContext(ctx, binary, "--config", configPath)
	command.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "gatus-history", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command, TerminateDuration: stdioShutdownTimeout}, nil)
	if err != nil {
		t.Fatalf("connect: %v\n%s", err, stderr.String())
	}
	defer func() { _ = session.Close() }()

	type source struct {
		Kind             string `json:"kind"`
		Status           string `json:"status"`
		Reason           string `json:"reason"`
		Complete         bool   `json:"complete"`
		ItemCount        int    `json:"item_count"`
		Discarded        int    `json:"discarded"`
		RetentionLimited bool   `json:"retention_limited"`
	}
	type body struct {
		Items []struct {
			Severity   string         `json:"severity"`
			Attributes map[string]any `json:"attributes"`
		} `json:"items"`
		Sources []source `json:"sources"`
	}
	call := func(name string, args map[string]any) body {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || result.IsError {
			t.Fatalf("%s failed: %v\n%s", name, err, stderr.String())
		}
		var out body
		executableE2EDecode(t, mustJSON(t, result.StructuredContent), &out)
		return out
	}
	gatusSource := func(out body) source {
		for _, entry := range out.Sources {
			if entry.Kind == "gatus" {
				return entry
			}
		}
		t.Fatalf("gatus source missing: %+v", out.Sources)
		return source{}
	}
	window := map[string]any{
		"start": now.Add(-time.Hour).Format(time.RFC3339),
		"end":   now.Add(-10 * time.Minute).Format(time.RFC3339),
	}

	history := call("incident_context", map[string]any{"service_id": "history", "start": window["start"], "end": window["end"]})
	if len(history.Items) != 2 || history.Items[1].Severity != "error" {
		t.Fatalf("valid history items=%+v", history.Items)
	}
	if got := gatusSource(history); got.Status != "ok" || got.Reason != "partial_invalid_records" ||
		got.Complete || !got.RetentionLimited || got.Discarded != 2 {
		t.Fatalf("history coverage=%+v", got)
	}

	missing := call("incident_context", map[string]any{"service_id": "missing", "start": window["start"], "end": window["end"]})
	if len(missing.Items) != 0 {
		t.Fatalf("missing endpoint produced history: %+v", missing.Items)
	}
	if got := gatusSource(missing); got.Status != "ok" || got.Reason != "unmatched_binding" || got.Complete {
		t.Fatalf("missing endpoint coverage=%+v", got)
	}

	empty := call("incident_context", map[string]any{"service_id": "empty", "start": window["start"], "end": window["end"]})
	if len(empty.Items) != 0 {
		t.Fatalf("empty endpoint produced history: %+v", empty.Items)
	}
	if got := gatusSource(empty); got.Reason != "retention_limited" || got.Complete || !got.RetentionLimited {
		t.Fatalf("empty endpoint coverage=%+v", got)
	}

	snapshot := call("service_status", map[string]any{"service_id": "incomplete"})
	if len(snapshot.Items) != 1 || snapshot.Items[0].Severity != "unknown" {
		t.Fatalf("missing success became a diagnosis: %+v", snapshot.Items)
	}
	if got := gatusSource(snapshot); got.Reason != "partial_invalid_records" || got.Complete || got.Discarded != 1 {
		t.Fatalf("incomplete snapshot coverage=%+v", got)
	}
}
