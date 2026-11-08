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

// This test detects a Loki response stream that contradicts the configured
// selector and would otherwise be attributed to the requested service.
func TestStdioLokiSelectorAttribution(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	t0 := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	loki := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/loki/api/v1/query_range" {
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"streams","result":[
			{"stream":{"container":"api"},"values":[["%d","level=info owned"]]},
			{"stream":{"container":"database"},"values":[["%d","level=error foreign"]]}
		]}}`, t0.UnixNano(), t0.UnixNano())
	}))
	defer loki.Close()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configText := fmt.Sprintf(`version: 1
limits:
  default_window: 15m
sources:
  loki:
    kind: loki
    base_url: %s
services:
  - id: api
    sources:
      loki: { source: loki, selector: '{container="api"}' }
`, loki.URL)
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}

	binary := stdioTestBinary(t, ctx)
	var stderr bytes.Buffer
	command := exec.CommandContext(ctx, binary, "--config", configPath)
	command.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "loki-attribution", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command, TerminateDuration: stdioShutdownTimeout}, nil)
	if err != nil {
		t.Fatalf("connect: %v\n%s", err, stderr.String())
	}
	defer func() { _ = session.Close() }()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "search_logs", Arguments: map[string]any{"service_id": "api"},
	})
	if err != nil || result.IsError {
		t.Fatalf("search_logs failed: %v\n%s", err, stderr.String())
	}
	var body struct {
		Items []struct {
			Summary    string         `json:"summary"`
			Attributes map[string]any `json:"attributes"`
		} `json:"items"`
		Source struct {
			Discarded int    `json:"discarded"`
			Reason    string `json:"reason"`
			Complete  bool   `json:"complete"`
		} `json:"source"`
	}
	executableE2EDecode(t, mustJSON(t, result.StructuredContent), &body)
	if len(body.Items) != 1 || body.Items[0].Attributes["line"] != "level=info owned" {
		t.Fatalf("foreign stream attributed to the service: %s", mustJSON(t, result.StructuredContent))
	}
	if body.Source.Discarded != 1 || body.Source.Complete || body.Source.Reason != "partial_invalid_records" {
		t.Fatalf("mismatched stream coverage=%+v", body.Source)
	}
	if bytes.Contains(mustJSON(t, result.StructuredContent), []byte("foreign")) {
		t.Fatalf("foreign log line leaked: %s", mustJSON(t, result.StructuredContent))
	}
}
