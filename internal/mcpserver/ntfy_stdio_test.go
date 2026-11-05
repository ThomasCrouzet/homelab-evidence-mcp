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

// This test detects priority normalization and presentation-severity drift.
func TestStdioNtfyPriorityNormalization(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	t0 := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	messages := func(lines ...string) *httptest.Server {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Query().Get("poll") != "1" {
				http.NotFound(w, r)
				return
			}
			for _, line := range lines {
				_, _ = fmt.Fprintln(w, line)
			}
		}))
		t.Cleanup(server.Close)
		return server
	}
	normal := messages(
		fmt.Sprintf(`{"id":"omitted","time":%d,"event":"message","message":"Backup completed successfully."}`, t0.Unix()),
		fmt.Sprintf(`{"id":"explicit","time":%d,"event":"message","message":"Backup completed successfully.","priority":3}`, t0.Unix()),
	)
	mixed := messages(
		fmt.Sprintf(`{"id":"min","time":%d,"event":"message","message":"minimum","priority":1}`, t0.Unix()),
		fmt.Sprintf(`{"id":"low","time":%d,"event":"message","message":"low","priority":2}`, t0.Unix()),
		fmt.Sprintf(`{"id":"high","time":%d,"event":"message","message":"high","priority":4}`, t0.Unix()),
		fmt.Sprintf(`{"id":"max","time":%d,"event":"message","message":"maximum","priority":5}`, t0.Unix()),
		fmt.Sprintf(`{"id":"invalid","time":%d,"event":"message","message":"invalid","priority":6}`, t0.Unix()),
	)

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configText := fmt.Sprintf(`version: 1
limits:
  default_window: 15m
  max_incident_window: 1h
sources:
  ntfy-normal:
    kind: ntfy
    base_url: %s
  ntfy-mixed:
    kind: ntfy
    base_url: %s
services:
  - id: normal
    sources:
      ntfy: { source: ntfy-normal, topic: normal-alerts }
  - id: mixed
    sources:
      ntfy: { source: ntfy-mixed, topic: mixed-alerts }
`, normal.URL, mixed.URL)
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}

	binary := stdioTestBinary(t, ctx)
	var stderr bytes.Buffer
	command := exec.CommandContext(ctx, binary, "--config", configPath)
	command.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "ntfy-fixture", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command, TerminateDuration: stdioShutdownTimeout}, nil)
	if err != nil {
		t.Fatalf("connect to executable: %v\nstderr:\n%s", err, stderr.String())
	}
	defer func() { _ = session.Close() }()

	normalResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "incident_context", Arguments: map[string]any{"service_id": "normal"},
	})
	if err != nil || normalResult.IsError {
		t.Fatalf("normal incident_context failed: %v", err)
	}
	var normalBody struct {
		Items []struct {
			Severity   string         `json:"severity"`
			Attributes map[string]any `json:"attributes"`
		} `json:"items"`
		FactualSummary string `json:"factual_summary"`
	}
	executableE2EDecode(t, mustJSON(t, normalResult.StructuredContent), &normalBody)
	if len(normalBody.Items) != 2 {
		t.Fatalf("items=%d: %s", len(normalBody.Items), mustJSON(t, normalResult.StructuredContent))
	}
	for _, item := range normalBody.Items {
		if item.Severity != "info" || item.Attributes["priority"] != float64(3) {
			t.Fatalf("normal notification was not informational: %+v", item)
		}
	}
	if !bytes.Contains([]byte(normalBody.FactualSummary), []byte("no error-severity items")) {
		t.Fatalf("normal notifications changed the summary: %s", normalBody.FactualSummary)
	}

	mixedResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "incident_context", Arguments: map[string]any{"service_id": "mixed"},
	})
	if err != nil || mixedResult.IsError {
		t.Fatalf("mixed incident_context failed: %v", err)
	}
	var mixedBody struct {
		Items []struct {
			SourceID   string         `json:"source_id"`
			Severity   string         `json:"severity"`
			Attributes map[string]any `json:"attributes"`
		} `json:"items"`
	}
	executableE2EDecode(t, mustJSON(t, mixedResult.StructuredContent), &mixedBody)
	want := map[string]string{
		"min": "info", "low": "info", "high": "error", "max": "critical", "invalid": "unknown",
	}
	if len(mixedBody.Items) != len(want) {
		t.Fatalf("items=%d, want=%d: %s", len(mixedBody.Items), len(want), mustJSON(t, mixedResult.StructuredContent))
	}
	for _, item := range mixedBody.Items {
		if item.Severity != want[item.SourceID] {
			t.Fatalf("%s severity=%s, want=%s", item.SourceID, item.Severity, want[item.SourceID])
		}
		if item.SourceID == "invalid" && item.Attributes["priority_valid"] != false {
			t.Fatalf("invalid priority was not explicit: %+v", item.Attributes)
		}
	}
}
