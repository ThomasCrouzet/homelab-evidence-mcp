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
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// This test detects Loki ordering that depends on opaque evidence identifiers.
func TestStdioLokiStableOrdering(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	t0 := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/loki/api/v1/query_range" {
			http.NotFound(w, r)
			return
		}
		values := [][]string{
			{strconv.FormatInt(t0.UnixNano(), 10), "first-same-time"},
			{strconv.FormatInt(t0.UnixNano(), 10), "second-same-time"},
			{strconv.FormatInt(t0.UnixNano(), 10), "second-same-time"},
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit > 0 && len(values) > limit {
			values = values[:limit]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"resultType": "streams",
				"result": []map[string]any{
					{"stream": map[string]string{"container": "one"}, "values": values},
					{"stream": map[string]string{"container": "two"}, "values": [][]string{
						{strconv.FormatInt(t0.UnixNano(), 10), "other-stream"},
					}},
				},
			},
		})
	}))
	defer upstream.Close()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configText := fmt.Sprintf(`version: 1
limits:
  default_window: 15m
  max_log_lines: 10
sources:
  loki:
    kind: loki
    base_url: %s
services:
  - id: logs
    sources:
      loki: { source: loki, selector: '{container="one"}' }
`, upstream.URL)
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}

	binary := stdioTestBinary(t, ctx)
	var stderr bytes.Buffer
	command := exec.CommandContext(ctx, binary, "--config", configPath)
	command.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "loki-order-fixture", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command, TerminateDuration: stdioShutdownTimeout}, nil)
	if err != nil {
		t.Fatalf("connect to executable: %v\nstderr:\n%s", err, stderr.String())
	}
	defer func() { _ = session.Close() }()

	type logResponse struct {
		Items []struct {
			ID         string         `json:"id"`
			Attributes map[string]any `json:"attributes"`
		} `json:"items"`
		Source struct {
			Truncated bool `json:"truncated"`
		} `json:"source"`
	}
	search := func(limit int) (logResponse, []byte) {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name: "search_logs", Arguments: map[string]any{
				"service_id": "logs", "duration": "1h", "limit": limit,
			},
		})
		if err != nil || result.IsError {
			t.Fatalf("search_logs failed: %v", err)
		}
		raw := mustJSON(t, result.StructuredContent)
		var body logResponse
		executableE2EDecode(t, raw, &body)
		return body, raw
	}

	firstSelected := ""
	for attempt := 0; attempt < 25; attempt++ {
		body, raw := search(1)
		if len(body.Items) != 1 || !body.Source.Truncated {
			t.Fatalf("attempt %d items=%d truncated=%v: %s", attempt, len(body.Items), body.Source.Truncated, raw)
		}
		line, _ := body.Items[0].Attributes["line"].(string)
		if attempt == 0 {
			firstSelected = line
		} else if line != firstSelected {
			t.Fatalf("attempt %d selected %q, first selected %q", attempt, line, firstSelected)
		}
	}
	if firstSelected == "" {
		t.Fatal("no line was selected")
	}

	want := []string{"first-same-time", "second-same-time", "second-same-time", "other-stream"}
	for attempt := 0; attempt < 3; attempt++ {
		body, raw := search(10)
		if len(body.Items) != len(want) {
			t.Fatalf("items=%d: %s", len(body.Items), raw)
		}
		for index, item := range body.Items {
			if line, _ := item.Attributes["line"].(string); line != want[index] {
				t.Fatalf("attempt %d position %d=%q, want=%q", attempt, index, line, want[index])
			}
		}
	}

	for attempt := 0; attempt < 2; attempt++ {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name: "incident_context", Arguments: map[string]any{"service_id": "logs"},
		})
		if err != nil || result.IsError {
			t.Fatalf("incident_context failed: %v", err)
		}
		var body logResponse
		executableE2EDecode(t, mustJSON(t, result.StructuredContent), &body)
		var got []string
		for _, item := range body.Items {
			if line, ok := item.Attributes["line"].(string); ok {
				got = append(got, line)
			}
		}
		if len(got) != len(want) {
			t.Fatalf("attempt %d incident items=%d, want=%d", attempt, len(got), len(want))
		}
		for index := range want {
			if got[index] != want[index] {
				t.Fatalf("attempt %d incident position %d=%q, want=%q", attempt, index, got[index], want[index])
			}
		}
	}
}
