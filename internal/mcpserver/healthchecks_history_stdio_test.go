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
				{"name": "backup", "slug": "backup", "tags": "mixedhist idmix", "status": "up", "unique_key": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
				{"name": "fail-check", "slug": "fail-check", "tags": "mixedhist", "status": "up", "unique_key": "dddddddddddddddddddddddddddddddd"},
				{"name": "empty-check", "slug": "empty-check", "status": "up", "unique_key": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
				{"name": "invalid-check", "slug": "invalid-check", "status": "up", "unique_key": "cccccccccccccccccccccccccccccccc"},
				{"name": "no-key", "slug": "no-key", "tags": "idmix", "status": "up"},
				{"name": "truncated", "slug": "truncated", "status": "up", "unique_key": "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"},
				{"name": "allowed", "slug": "allowed", "tags": "authmix", "status": "up", "unique_key": "11111111111111111111111111111111"},
				{"name": "denied", "slug": "denied", "tags": "authmix", "status": "up", "unique_key": "22222222222222222222222222222222"},
				{"name": "database", "slug": "db-primary", "status": "down", "unique_key": "33333333333333333333333333333333"},
				{"name": "nightly backup", "slug": "database", "status": "down", "unique_key": "44444444444444444444444444444444"},
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
			case "22222222222222222222222222222222":
				w.WriteHeader(http.StatusForbidden)
			case "11111111111111111111111111111111":
				_ = json.NewEncoder(w).Encode(map[string]any{"flips": []map[string]any{
					{"timestamp": downAt.Format(time.RFC3339), "up": 0},
				}})
			case "cccccccccccccccccccccccccccccccc":
				_ = json.NewEncoder(w).Encode(map[string]any{"flips": []map[string]any{
					{"timestamp": "invalid", "up": 0},
				}})
			case "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee":
				flips := make([]map[string]any, 0, 201)
				for index := 0; index < 201; index++ {
					flips = append(flips, map[string]any{
						"timestamp": now.Add(-19*time.Minute + time.Duration(index)*time.Second).Format(time.RFC3339),
						"up":        index % 2,
					})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"flips": flips})
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
  - id: truncated
    sources:
      healthchecks: { source: hc, check_name: truncated, history_mode: transitions }
  - id: partial-auth
    sources:
      healthchecks: { source: hc, check_tags: [authmix], history_mode: transitions }
  - id: mixed-missing
    sources:
      healthchecks: { source: hc, check_tags: [idmix], history_mode: transitions }
  - id: ambiguous-name
    sources:
      healthchecks: { source: hc, check_name: database, history_mode: transitions }
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
		Complete  bool   `json:"complete"`
		TimeBasis string `json:"time_basis"`
		Items     []item `json:"items"`
		Sources   []struct {
			Kind               string `json:"kind"`
			Status             string `json:"status"`
			Reason             string `json:"reason"`
			Complete           bool   `json:"complete"`
			Discarded          int    `json:"discarded"`
			UnauthorizedChecks int    `json:"unauthorized_checks"`
			MissingChecks      int    `json:"missing_identity_checks"`
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
	type sourceView struct {
		status, reason          string
		complete                bool
		discarded, unauthorized int
		missing                 int
	}
	find := func(body outcome, kind string) sourceView {
		for _, source := range body.Sources {
			if source.Kind == kind {
				return sourceView{
					status: source.Status, reason: source.Reason, complete: source.Complete,
					discarded: source.Discarded, unauthorized: source.UnauthorizedChecks,
					missing: source.MissingChecks,
				}
			}
		}
		t.Fatalf("source %s missing", kind)
		return sourceView{}
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
	if recovered.TimeBasis != "historical_transitions" {
		t.Fatalf("time_basis=%q", recovered.TimeBasis)
	}
	recoveredSource := find(recovered, "healthchecks")
	if recoveredSource.status != "ok" || recoveredSource.reason != "unknown_history_coverage" || recoveredSource.complete {
		t.Fatalf("history coverage status=%s reason=%s complete=%v", recoveredSource.status, recoveredSource.reason, recoveredSource.complete)
	}

	snapshot := call("service_status", map[string]any{"service_id": "snapshot"})
	if len(snapshot.Items) != 1 || snapshot.Items[0].Severity != "info" ||
		snapshot.Items[0].Attributes["status"] != "up" {
		t.Fatalf("service_status changed to history: %+v", snapshot.Items)
	}
	if snap := find(snapshot, "healthchecks"); snap.reason != "" || !snap.complete {
		t.Fatalf("snapshot coverage reason=%s complete=%v", snap.reason, snap.complete)
	}

	empty := call("failed_crons", map[string]any{"service_id": "empty", "start": windowStart, "end": windowEnd})
	if len(empty.Items) != 0 {
		t.Fatalf("empty history items=%+v", empty.Items)
	}
	if emptySource := find(empty, "healthchecks"); emptySource.reason != "unknown_history_coverage" || emptySource.complete {
		t.Fatalf("empty history reason=%s complete=%v", emptySource.reason, emptySource.complete)
	}

	invalid := call("failed_crons", map[string]any{"service_id": "invalid", "start": windowStart, "end": windowEnd})
	if len(invalid.Items) != 0 {
		t.Fatalf("invalid history items=%+v", invalid.Items)
	}
	if invalidSource := find(invalid, "healthchecks"); invalidSource.discarded != 1 {
		t.Fatalf("invalid history discarded=%d", invalidSource.discarded)
	}

	partial := call("failed_crons", map[string]any{"service_id": "partial", "start": windowStart, "end": windowEnd})
	if len(partial.Items) != 2 {
		t.Fatalf("partial history items=%+v", partial.Items)
	}
	if partialSource := find(partial, "healthchecks"); partialSource.status != "ok" ||
		partialSource.reason != "partial_fetch" || partialSource.complete {
		t.Fatalf("partial fetch status=%s reason=%s complete=%v", partialSource.status, partialSource.reason, partialSource.complete)
	}

	missing := call("failed_crons", map[string]any{"service_id": "missing", "start": windowStart, "end": windowEnd})
	if missingSource := find(missing, "healthchecks"); missingSource.status != "ok" ||
		missingSource.reason != "missing_identity" || missingSource.complete || len(missing.Items) != 0 || missingSource.missing != 1 {
		t.Fatalf("missing identity status=%s reason=%s complete=%v items=%d missing=%d", missingSource.status, missingSource.reason, missingSource.complete, len(missing.Items), missingSource.missing)
	}

	truncated := call("failed_crons", map[string]any{"service_id": "truncated", "start": windowStart, "end": windowEnd})
	if truncatedSource := find(truncated, "healthchecks"); truncatedSource.status != "ok" ||
		truncatedSource.reason != "truncated" || truncatedSource.complete ||
		len(truncated.Items) == 0 || len(truncated.Items) > 200 {
		t.Fatalf("truncated history status=%s reason=%s complete=%v items=%d",
			truncatedSource.status, truncatedSource.reason, truncatedSource.complete, len(truncated.Items))
	}

	partialAuth := call("failed_crons", map[string]any{"service_id": "partial-auth", "start": windowStart, "end": windowEnd})
	if len(partialAuth.Items) != 1 {
		t.Fatalf("partial auth lost evidence: %+v", partialAuth.Items)
	}
	if authSource := find(partialAuth, "healthchecks"); authSource.status != "ok" ||
		authSource.reason != "authentication_failed" || authSource.complete || authSource.unauthorized != 1 {
		t.Fatalf("partial auth status=%s reason=%s complete=%v unauthorized=%d",
			authSource.status, authSource.reason, authSource.complete, authSource.unauthorized)
	}

	mixedMissing := call("failed_crons", map[string]any{"service_id": "mixed-missing", "start": windowStart, "end": windowEnd})
	if len(mixedMissing.Items) != 2 {
		t.Fatalf("mixed missing-identity items=%+v", mixedMissing.Items)
	}
	if mixSource := find(mixedMissing, "healthchecks"); mixSource.status != "ok" ||
		mixSource.reason != "missing_identity" || mixSource.complete || mixSource.missing != 1 {
		t.Fatalf("mixed missing-identity status=%s reason=%s complete=%v missing=%d",
			mixSource.status, mixSource.reason, mixSource.complete, mixSource.missing)
	}

	ambiguous := call("failed_crons", map[string]any{"service_id": "ambiguous-name", "start": windowStart, "end": windowEnd})
	if len(ambiguous.Items) != 0 {
		t.Fatalf("ambiguous binding attributed other check evidence: %+v", ambiguous.Items)
	}
	if ambSource := find(ambiguous, "healthchecks"); ambSource.status != "error" || ambSource.reason != "ambiguous_binding" {
		t.Fatalf("ambiguous binding status=%s reason=%s", ambSource.status, ambSource.reason)
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
