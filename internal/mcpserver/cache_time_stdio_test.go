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
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// This test detects a cache hit that moves a snapshot observation time.
func TestStdioSnapshotTimeStableAcrossCacheHits(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/containers/json" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		time.Sleep(150 * time.Millisecond)
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"Names": []string{"/stable-app"}, "Image": "example/stable:1",
			"State": "running", "Status": "Up 2 hours",
		}})
	}))
	defer upstream.Close()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configText := fmt.Sprintf(`version: 1
limits:
  source_cache_ttl: 1s
  per_source_timeout: 5s
  total_timeout: 20s
sources:
  docker:
    kind: docker
    base_url: %s
services:
  - id: stable
    sources:
      docker: { source: docker, container_name: stable-app }
`, upstream.URL)
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}

	binary := stdioTestBinary(t, ctx)
	var stderr bytes.Buffer
	command := exec.CommandContext(ctx, binary, "--config", configPath)
	command.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "cache-time-fixture", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command, TerminateDuration: stdioShutdownTimeout}, nil)
	if err != nil {
		t.Fatalf("connect to executable: %v\nstderr:\n%s", err, stderr.String())
	}
	defer func() { _ = session.Close() }()

	type snapshot struct {
		ObservedAt  time.Time `json:"observed_at"`
		RetrievedAt time.Time `json:"retrieved_at"`
	}
	call := func() snapshot {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name: "service_status", Arguments: map[string]any{"service_id": "stable"},
		})
		if err != nil || result.IsError {
			t.Fatalf("service_status failed: %v", err)
		}
		var body struct {
			Items []snapshot `json:"items"`
		}
		executableE2EDecode(t, mustJSON(t, result.StructuredContent), &body)
		if len(body.Items) != 1 {
			t.Fatalf("items=%d: %s", len(body.Items), mustJSON(t, result.StructuredContent))
		}
		return body.Items[0]
	}

	first := call()
	second := call()
	if !first.ObservedAt.Equal(second.ObservedAt) {
		t.Fatalf("cache hit moved observed_at: %s != %s", second.ObservedAt, first.ObservedAt)
	}
	if !second.RetrievedAt.After(first.RetrievedAt) && !second.RetrievedAt.Equal(first.RetrievedAt) {
		t.Fatalf("retrieved_at went backwards: %s -> %s", first.RetrievedAt, second.RetrievedAt)
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls before expiry=%d, want=1", calls.Load())
	}

	time.Sleep(1200 * time.Millisecond)
	third := call()
	if calls.Load() != 2 {
		t.Fatalf("upstream calls after expiry=%d, want=2", calls.Load())
	}
	if !third.ObservedAt.After(first.ObservedAt) {
		t.Fatalf("refresh did not record a new snapshot time: %s", third.ObservedAt)
	}
}
