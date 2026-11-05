package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// This test detects snapshot-only behavior, lost transitions, and hidden
// partial healthchecks history failures.
func TestStdioHealthchecksTransitions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	now := time.Now().UTC().Truncate(time.Second)
	downAt := now.Add(-15 * time.Minute)
	upAt := now.Add(-10 * time.Minute)
	windowStart := now.Add(-20 * time.Minute).Format(time.RFC3339)
	windowEnd := now.Add(-time.Minute).Format(time.RFC3339)

	var nonGETs atomic.Int32
	hc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			nonGETs.Add(1)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == "/api/v3/checks/" {
			_ = json.NewEncoder(w).Encode(map[string]any{"checks": []map[string]any{
				{"name": "backup", "slug": "backup", "tags": "mixedhist", "status": "up", "unique_key": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
				{"name": "fail-check", "slug": "fail-check", "tags": "mixedhist", "status": "up", "unique_key": "dddddddddddddddddddddddddddddddd"},
				{"name": "empty-check", "slug": "empty-check", "status": "up", "unique_key": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
				{"name": "invalid-check", "slug": "invalid-check", "status": "up", "unique_key": "cccccccccccccccccccccccccccccccc"},
				{"name": "no-key", "slug": "no-key", "status": "up"},
			}})
			return
		}
		const prefix = "/api/v3/checks/"
		if strings.HasPrefix(r.URL.Path, prefix) && strings.HasSuffix(r.URL.Path, "/flips/") {
			key := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix), "/flips/")
			switch key {
			case "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa":
				_ = json.NewEncoder(w).Encode(map[string]any{"flips": []map[string]any{
					{"timestamp": downAt.Format(time.RFC3339), "up": 0},
					{"timestamp": upAt.Format(time.RFC3339), "up": 1},
				}})
			case "dddddddddddddddddddddddddddddddd":
				w.WriteHeader(http.StatusInternalServerError)
			case "cccccccccccccccccccccccccccccccc":
				_ = json.NewEncoder(w).Encode(map[string]any{"flips": []map[string]any{
					{"timestamp": "invalid", "up": 0},
				}})
			default:
				_ = json.NewEncoder(w).Encode(map[string]any{"flips": []map[string]any{}})
			}
			return
		}
		http.NotFound(w, r)
	}))
	defer hc.Close()
	gatus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			nonGETs.Add(1)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"name": "api", "key": "api",
			"results": []map[string]any{{"success": true, "status": 200, "timestamp": now.Add(-2 * time.Minute).Format(time.RFC3339)}},
		}})
	}))
	defer gatus.Close()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configText := fmt.Sprintf(`version: 1
limits:
  max_cron_window: 24h
  per_source_timeout: 2s
  total_timeout: 10s
sources:
  hc:
    kind: healthchecks
    base_url: %s
    token_env: HEM_HC_TOKEN
  gatus:
    kind: gatus
    base_url: %s
services:
  - id: recovered
    sources:
      healthchecks: { source: hc, check_name: backup, history_mode: transitions }
  - id: snapshot
    sources:
      healthchecks: { source: hc, check_name: backup }
  - id: empty
    sources:
      healthchecks: { source: hc, check_name: empty-check, history_mode: transitions }
  - id: invalid
    sources:
      healthchecks: { source: hc, check_name: invalid-check, history_mode: transitions }
  - id: partial
    sources:
      healthchecks: { source: hc, check_tags: [mixedhist], history_mode: transitions }
  - id: missing
    sources:
      healthchecks: { source: hc, check_name: no-key, history_mode: transitions }
  - id: mixed
    sources:
      healthchecks: { source: hc, check_name: backup, history_mode: transitions }
      gatus: { source: gatus, endpoint_key: api }
`, hc.URL, gatus.URL)
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}

	binary := stdioTestBinary(t, ctx)
	var stderr bytes.Buffer
	command := exec.CommandContext(ctx, binary, "--config", configPath)
	command.Env = append(os.Environ(), "HEM_HC_TOKEN=fixture-token")
	command.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "healthchecks-history", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command, TerminateDuration: stdioShutdownTimeout}, nil)
	if err != nil {
		t.Fatalf("connect: %v\n%s", err, stderr.String())
	}
	defer func() { _ = session.Close() }()

	type item struct {
		Source      string         `json:"source"`
		ObservedAt  time.Time      `json:"observed_at"`
		Severity    string         `json:"severity"`
		Attributes  map[string]any `json:"attributes"`
		WindowStart *time.Time     `json:"window_start"`
	}
	type outcome struct {
		Complete bool   `json:"complete"`
		Items    []item `json:"items"`
		Sources  []struct {
			Kind      string `json:"kind"`
			Status    string `json:"status"`
			Reason    string `json:"reason"`
			Complete  bool   `json:"complete"`
			Discarded int    `json:"discarded"`
		} `json:"sources"`
	}
	call := func(name string, args map[string]any) outcome {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || result.IsError {
			t.Fatalf("%s failed: %v\n%s", name, err, stderr.String())
		}
		var body outcome
		executableE2EDecode(t, mustJSON(t, result.StructuredContent), &body)
		return body
	}
	find := func(body outcome, kind string) (string, string, bool, int) {
		for _, source := range body.Sources {
			if source.Kind == kind {
				return source.Status, source.Reason, source.Complete, source.Discarded
			}
		}
		t.Fatalf("source %s missing", kind)
		return "", "", false, 0
	}
	recovered := call("failed_crons", map[string]any{
		"service_id": "recovered", "start": windowStart, "end": windowEnd,
	})
	if len(recovered.Items) != 2 || recovered.Items[0].Severity != "error" || recovered.Items[1].Severity != "info" {
		t.Fatalf("recovered incident items=%+v", recovered.Items)
	}
	if recovered.Items[0].Attributes["transition"] != "down" || recovered.Items[1].Attributes["transition"] != "up" {
		t.Fatalf("transitions=%+v", recovered.Items)
	}
	if recovered.Items[0].WindowStart == nil || recovered.Items[1].WindowStart == nil {
		t.Fatal("history items have no window")
	}
	status, reason, complete, _ := find(recovered, "healthchecks")
	if status != "ok" || reason != "unknown_history_coverage" || complete {
		t.Fatalf("history coverage status=%s reason=%s complete=%v", status, reason, complete)
	}

	snapshot := call("service_status", map[string]any{"service_id": "snapshot"})
	if len(snapshot.Items) != 1 || snapshot.Items[0].Severity != "info" ||
		snapshot.Items[0].Attributes["status"] != "up" {
		t.Fatalf("service_status changed to history: %+v", snapshot.Items)
	}
	if _, snapReason, snapComplete, _ := find(snapshot, "healthchecks"); snapReason != "" || !snapComplete {
		t.Fatalf("snapshot coverage reason=%s complete=%v", snapReason, snapComplete)
	}

	empty := call("failed_crons", map[string]any{"service_id": "empty", "start": windowStart, "end": windowEnd})
	if len(empty.Items) != 0 {
		t.Fatalf("empty history items=%+v", empty.Items)
	}
	if _, emptyReason, emptyComplete, _ := find(empty, "healthchecks"); emptyReason != "unknown_history_coverage" || emptyComplete {
		t.Fatalf("empty history reason=%s complete=%v", emptyReason, emptyComplete)
	}

	invalid := call("failed_crons", map[string]any{"service_id": "invalid", "start": windowStart, "end": windowEnd})
	if len(invalid.Items) != 0 {
		t.Fatalf("invalid history items=%+v", invalid.Items)
	}
	if _, _, _, discarded := find(invalid, "healthchecks"); discarded != 1 {
		t.Fatalf("invalid history discarded=%d", discarded)
	}

	partial := call("failed_crons", map[string]any{"service_id": "partial", "start": windowStart, "end": windowEnd})
	if len(partial.Items) != 2 {
		t.Fatalf("partial history items=%+v", partial.Items)
	}
	if status, partialReason, partialComplete, _ := find(partial, "healthchecks"); status != "ok" ||
		partialReason != "partial_fetch" || partialComplete {
		t.Fatalf("partial fetch status=%s reason=%s complete=%v", status, partialReason, partialComplete)
	}

	missing := call("failed_crons", map[string]any{"service_id": "missing", "start": windowStart, "end": windowEnd})
	if status, missingReason, missingComplete, _ := find(missing, "healthchecks"); status != "ok" ||
		missingReason != "missing_identity" || missingComplete || len(missing.Items) != 0 {
		t.Fatalf("missing identity status=%s reason=%s complete=%v items=%d", status, missingReason, missingComplete, len(missing.Items))
	}

	mixed := call("incident_context", map[string]any{"service_id": "mixed", "start": windowStart, "end": windowEnd})
	sources := map[string]int{}
	for _, entry := range mixed.Items {
		sources[entry.Source]++
	}
	if sources["gatus"] != 1 || sources["healthchecks"] != 2 || mixed.Complete {
		t.Fatalf("mixed history sources=%+v complete=%v", sources, mixed.Complete)
	}
	if nonGETs.Load() != 0 {
		t.Fatalf("upstreams received %d non-GET requests", nonGETs.Load())
	}
}
