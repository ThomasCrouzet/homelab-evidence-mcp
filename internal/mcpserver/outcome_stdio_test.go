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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type outcomeSource struct {
	Kind       string `json:"kind"`
	Status     string `json:"status"`
	Reason     string `json:"reason"`
	Complete   bool   `json:"complete"`
	Truncated  bool   `json:"truncated"`
	Discarded  int    `json:"discarded"`
	CacheState string `json:"cache_state"`
	CacheAgeS  int64  `json:"cache_age_seconds"`
	ItemCount  int    `json:"item_count"`
	Error      string `json:"error"`
}

type outcomeBody struct {
	Complete  bool `json:"complete"`
	Truncated bool `json:"truncated"`
	Items     []struct {
		Source     string         `json:"source"`
		Attributes map[string]any `json:"attributes"`
	} `json:"items"`
	Sources []outcomeSource `json:"sources"`
	Source  outcomeSource   `json:"source"`
}

// This test detects missing coverage reasons, wrong completeness, and a
// single-source failure that looks successful.
func TestStdioOutcomeCoverage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	base := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)

	gatusServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/endpoints/statuses" {
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprintf(w, `[{"name":"present","key":"present","results":[{"success":true,"status":200,"timestamp":%q}]}]`, base.Format(time.RFC3339))
	}))
	defer gatusServer.Close()
	slowGatus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Second)
		_, _ = fmt.Fprintf(w, `[{"name":"slow","key":"slow","results":[{"success":true,"status":200,"timestamp":%q}]}]`, base.Format(time.RFC3339))
	}))
	defer slowGatus.Close()
	dockerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/containers/json" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"Names": []string{"/mixed-app"}, "Image": "example/mixed:1",
			"State": "running", "Status": "Up 2 hours",
		}})
	}))
	defer dockerServer.Close()
	lokiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/loki/api/v1/query_range" {
			http.NotFound(w, r)
			return
		}
		query := r.URL.Query().Get("query")
		switch {
		case strings.Contains(query, `container="fail"`):
			w.WriteHeader(http.StatusServiceUnavailable)
		case strings.Contains(query, `container="empty"`):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success",
				"data":   map[string]any{"resultType": "streams", "result": []any{}},
			})
		case strings.Contains(query, `container="partial"`):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success",
				"data": map[string]any{"resultType": "streams", "result": []map[string]any{{
					"stream": map[string]string{"container": "partial"},
					"values": [][]string{
						{strconv.FormatInt(base.UnixNano(), 10), "level=error usable line"},
						{"invalid", "discarded line"},
					},
				}}},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success",
				"data":   map[string]any{"resultType": "streams", "result": []any{}},
			})
		}
	}))
	defer lokiServer.Close()

	dir := t.TempDir()
	auditPath := filepath.Join(dir, "audit.jsonl")
	configPath := filepath.Join(dir, "config.yaml")
	configText := fmt.Sprintf(`version: 1
limits:
  default_window: 15m
  max_incident_window: 1h
  max_log_lines: 10
  per_source_timeout: 300ms
  total_timeout: 10s
  source_cache_ttl: 1m
audit:
  file: %s
sources:
  gatus:
    kind: gatus
    base_url: %s
  gatus-slow:
    kind: gatus
    base_url: %s
  docker:
    kind: docker
    base_url: %s
  loki:
    kind: loki
    base_url: %s
services:
  - id: gatus-good
    sources:
      gatus: { source: gatus, endpoint_key: present }
  - id: gatus-unmatched
    sources:
      gatus: { source: gatus, endpoint_key: missing }
  - id: gatus-timeout
    sources:
      gatus: { source: gatus-slow, endpoint_key: slow }
  - id: docker-current
    sources:
      docker: { source: docker, container_name: mixed-app }
  - id: loki-empty
    sources:
      loki: { source: loki, selector: '{container="empty"}' }
  - id: loki-partial
    sources:
      loki: { source: loki, selector: '{container="partial"}' }
  - id: loki-fail
    sources:
      loki: { source: loki, selector: '{container="fail"}' }
  - id: mixed
    sources:
      docker: { source: docker, container_name: mixed-app }
      loki: { source: loki, selector: '{container="fail"}' }
`, auditPath, gatusServer.URL, slowGatus.URL, dockerServer.URL, lokiServer.URL)
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}

	binary := stdioTestBinary(t, ctx)
	var stderr bytes.Buffer
	command := exec.CommandContext(ctx, binary, "--config", configPath)
	command.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "outcome-fixture", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command, TerminateDuration: stdioShutdownTimeout}, nil)
	if err != nil {
		t.Fatalf("connect to executable: %v\nstderr:\n%s", err, stderr.String())
	}

	call := func(name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return result
	}
	decode := func(result *mcp.CallToolResult) outcomeBody {
		t.Helper()
		if result.IsError {
			t.Fatalf("unexpected error result: %s", mustJSON(t, result))
		}
		var body outcomeBody
		executableE2EDecode(t, mustJSON(t, result.StructuredContent), &body)
		return body
	}
	find := func(body outcomeBody, kind string) outcomeSource {
		t.Helper()
		for _, source := range body.Sources {
			if source.Kind == kind {
				return source
			}
		}
		t.Fatalf("source %s is missing from %+v", kind, body.Sources)
		return outcomeSource{}
	}

	good := decode(call("service_status", map[string]any{"service_id": "gatus-good"}))
	if !good.Complete {
		t.Fatalf("complete source reported incomplete: %+v", good)
	}
	absent := find(good, "docker")
	if absent.Status != "absent" || absent.Reason != "no_binding" || absent.Complete != true {
		t.Fatalf("absent source outcome=%+v", absent)
	}

	unmatched := decode(call("service_status", map[string]any{"service_id": "gatus-unmatched"}))
	unmatchedSource := find(unmatched, "gatus")
	if unmatched.Complete || unmatchedSource.Reason != "unmatched_binding" || unmatchedSource.Status != "ok" {
		t.Fatalf("unmatched binding=%+v source=%+v", unmatched, unmatchedSource)
	}

	timeout := decode(call("service_status", map[string]any{"service_id": "gatus-timeout"}))
	timeoutSource := find(timeout, "gatus")
	if timeout.Complete || timeoutSource.Status != "timeout" || timeoutSource.Reason != "timeout" {
		t.Fatalf("timeout outcome=%+v source=%+v", timeout, timeoutSource)
	}

	first := decode(call("service_status", map[string]any{"service_id": "docker-current"}))
	time.Sleep(1200 * time.Millisecond)
	second := decode(call("service_status", map[string]any{"service_id": "docker-current"}))
	dockerSource := find(second, "docker")
	if dockerSource.CacheState != "hit" || dockerSource.CacheAgeS < 1 {
		t.Fatalf("warm cache reporting=%+v", dockerSource)
	}
	if !first.Complete || !second.Complete {
		t.Fatalf("snapshot source reported incomplete: first=%+v second=%+v", first, second)
	}

	emptyResult := call("search_logs", map[string]any{"service_id": "loki-empty", "duration": "1h"})
	if emptyResult.IsError {
		t.Fatalf("valid empty search failed: %s", mustJSON(t, emptyResult))
	}
	empty := decode(emptyResult)
	if !empty.Complete || empty.Source.Status != "ok" || empty.Source.Reason != "empty_result" || len(empty.Items) != 0 {
		t.Fatalf("empty search outcome=%+v", empty)
	}

	partial := decode(call("search_logs", map[string]any{"service_id": "loki-partial", "duration": "1h"}))
	if partial.Complete || partial.Source.Reason != "partial_invalid_records" ||
		partial.Source.Discarded != 1 || len(partial.Items) != 1 {
		t.Fatalf("partial invalid data outcome=%+v", partial)
	}

	failed := call("search_logs", map[string]any{"service_id": "loki-fail", "duration": "1h"})
	if !failed.IsError {
		t.Fatalf("Loki 503 was not an error result: %s", mustJSON(t, failed))
	}
	failedRaw := mustJSON(t, failed)
	if !bytes.Contains(failedRaw, []byte("reason")) || !bytes.Contains(failedRaw, []byte("upstream_error")) {
		t.Fatalf("Loki 503 has no machine-readable cause: %s", failedRaw)
	}

	mixed := decode(call("incident_context", map[string]any{"service_id": "mixed"}))
	mixedDocker := find(mixed, "docker")
	mixedLoki := find(mixed, "loki")
	if mixed.Complete || mixedLoki.Status != "error" || mixedLoki.Reason != "upstream_error" ||
		mixedDocker.Status != "ok" || len(mixed.Items) == 0 {
		t.Fatalf("mixed failure=%+v docker=%+v loki=%+v", mixed, mixedDocker, mixedLoki)
	}

	allFailed := decode(call("incident_context", map[string]any{"service_id": "loki-fail"}))
	if allFailed.Complete || len(allFailed.Items) != 0 || find(allFailed, "loki").Status != "error" {
		t.Fatalf("all-source failure=%+v", allFailed)
	}

	historical := decode(call("incident_context", map[string]any{
		"service_id": "docker-current",
		"start":      time.Now().UTC().Add(-3 * time.Hour).Format(time.RFC3339),
		"end":        time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339),
	}))
	historicalDocker := find(historical, "docker")
	if historical.Complete || historicalDocker.Reason != "unsupported_history" {
		t.Fatalf("historical snapshot=%+v source=%+v", historical, historicalDocker)
	}

	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	assertAuditStatuses(t, auditPath)
}

func assertAuditStatuses(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	type event struct {
		Tool      string `json:"tool"`
		ServiceID string `json:"service_id"`
		Status    string `json:"status"`
	}
	found := map[string]string{}
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var entry event
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatalf("invalid audit record: %v", err)
		}
		found[entry.Tool+"/"+entry.ServiceID] = entry.Status
	}
	want := map[string]string{
		"service_status/gatus-good":      "ok",
		"service_status/gatus-unmatched": "partial",
		"service_status/gatus-timeout":   "error",
		"incident_context/mixed":         "partial",
		"incident_context/loki-fail":     "error",
		"search_logs/loki-empty":         "ok",
		"search_logs/loki-partial":       "partial",
		"search_logs/loki-fail":          "error",
	}
	for key, status := range want {
		if found[key] != status {
			t.Fatalf("audit %s status=%q, want=%q: %+v", key, found[key], status, found)
		}
	}
}
