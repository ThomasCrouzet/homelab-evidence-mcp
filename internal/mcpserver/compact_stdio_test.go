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
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// This test detects a noisy source that hides another source in compact mode,
// an unenforced byte budget, and unstable selection.
func TestStdioCompactIncidentContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	t0 := time.Now().UTC().Truncate(time.Second)
	gatus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/endpoints/statuses" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"name": "api", "key": "api",
			"results": []map[string]any{{
				"success": true, "status": 200, "timestamp": t0.Add(-5 * time.Minute).Format(time.RFC3339),
			}},
		}})
	}))
	defer gatus.Close()
	loki := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/loki/api/v1/query_range" {
			http.NotFound(w, r)
			return
		}
		values := make([][]string, 0, 20)
		for index := 0; index < 20; index++ {
			line := fmt.Sprintf("line-%02d %s", index, strings.Repeat("é", 250))
			if index == 19 {
				line = "level=error conflicting " + strings.Repeat("é", 250)
			} else if index >= 17 {
				line = "duplicate " + strings.Repeat("é", 250)
			}
			values = append(values, []string{
				strconv.FormatInt(t0.Add(-time.Minute).Add(time.Duration(index)*time.Second).UnixNano(), 10),
				line,
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{"resultType": "streams", "result": []map[string]any{{
				"stream": map[string]string{"container": "api"}, "values": values,
			}}},
		})
	}))
	defer loki.Close()

	type compactBody struct {
		Complete            bool     `json:"complete"`
		Truncated           bool     `json:"truncated"`
		Compact             bool     `json:"compact"`
		ByteBudget          int      `json:"byte_budget"`
		ByteSize            int      `json:"byte_size"`
		SelectionTruncated  bool     `json:"selection_truncated"`
		ByteBudgetTruncated bool     `json:"byte_budget_truncated"`
		OmittedItems        int      `json:"omitted_items"`
		OmittedSources      []string `json:"omitted_sources"`
		Items               []struct {
			ID         string `json:"id"`
			Source     string `json:"source"`
			Severity   string `json:"severity"`
			Attributes struct {
				Line string `json:"line"`
			} `json:"attributes"`
		} `json:"items"`
	}

	run := func(configBudget int, maxItems int, compact bool) compactBody {
		t.Helper()
		configPath := filepath.Join(t.TempDir(), "config.yaml")
		configText := fmt.Sprintf(`version: 1
limits:
  default_window: 15m
  max_incident_window: 1h
  max_log_lines: 30
  max_evidence_items: 30
  max_incident_response_bytes: %d
  per_source_timeout: 2s
  total_timeout: 10s
sources:
  gatus:
    kind: gatus
    base_url: %s
  loki:
    kind: loki
    base_url: %s
services:
  - id: noisy
    sources:
      gatus: { source: gatus, endpoint_key: api }
      loki: { source: loki, selector: '{container="api"}' }
`, configBudget, gatus.URL, loki.URL)
		if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
			t.Fatal(err)
		}
		binary := stdioTestBinary(t, ctx)
		var stderr bytes.Buffer
		command := exec.CommandContext(ctx, binary, "--config", configPath)
		command.Stderr = &stderr
		client := mcp.NewClient(&mcp.Implementation{Name: "compact-fixture", Version: "1"}, nil)
		session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command, TerminateDuration: stdioShutdownTimeout}, nil)
		if err != nil {
			t.Fatalf("connect: %v\n%s", err, stderr.String())
		}
		defer func() { _ = session.Close() }()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name: "incident_context", Arguments: map[string]any{
				"service_id": "noisy", "max_items": maxItems, "compact": compact,
			},
		})
		if err != nil || result.IsError {
			t.Fatalf("incident_context failed: %v\n%s", err, stderr.String())
		}
		raw := mustJSON(t, result.StructuredContent)
		if !utf8.Valid(raw) {
			t.Fatal("compact result is not valid UTF-8")
		}
		var body compactBody
		executableE2EDecode(t, raw, &body)
		if body.ByteSize > configBudget {
			t.Fatalf("byte size=%d exceeds budget=%d", body.ByteSize, configBudget)
		}
		for _, item := range body.Items {
			cached, err := session.CallTool(ctx, &mcp.CallToolParams{
				Name: "get_evidence", Arguments: map[string]any{"id": item.ID},
			})
			if err != nil || cached.IsError {
				t.Fatalf("selected evidence %s is not retrievable: %v", item.ID, err)
			}
		}
		return body
	}

	defaultBody := run(65536, 5, false)
	if defaultBody.Compact || !defaultBody.SelectionTruncated || defaultBody.OmittedItems != 16 {
		t.Fatalf("default selection completeness changed: %+v", defaultBody)
	}
	for _, item := range defaultBody.Items {
		if item.Source == "gatus" {
			t.Fatalf("default newest selection unexpectedly kept gatus: %+v", defaultBody.Items)
		}
	}

	compact := run(65536, 5, true)
	if !compact.Compact || !compact.SelectionTruncated ||
		compact.OmittedItems != 16 || compact.ByteBudgetTruncated {
		t.Fatalf("compact selection=%+v", compact)
	}
	sources := map[string]int{}
	severities := map[string]bool{}
	duplicates := 0
	for _, item := range compact.Items {
		sources[item.Source]++
		severities[item.Severity] = true
		if strings.HasPrefix(item.Attributes.Line, "duplicate ") {
			duplicates++
		}
	}
	if sources["gatus"] != 1 || sources["loki"] != 4 {
		t.Fatalf("per-source reservation failed: %+v", sources)
	}
	if duplicates != 2 {
		t.Fatalf("repeated records were not preserved: %d", duplicates)
	}
	if !severities["info"] || !severities["error"] {
		t.Fatalf("conflicting observations were hidden: %+v", severities)
	}

	tiny := run(8192, 30, true)
	if !tiny.Compact || !tiny.ByteBudgetTruncated || tiny.OmittedItems == 0 ||
		len(tiny.OmittedSources) == 0 || tiny.ByteSize > 8192 {
		t.Fatalf("tiny budget result=%+v", tiny)
	}
}
