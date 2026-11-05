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

// This test detects a lost ntfy replay-limit signal on a cache hit and false
// truncation warnings for complete responses.
func TestStdioNtfyTruncationOnCacheHits(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	t0 := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Query().Get("poll") != "1" {
			http.NotFound(w, r)
			return
		}
		upstreamCalls.Add(1)
		w.Header().Set("X-Messages-Truncated", "1")
		w.Header().Set("Set-Cookie", "sid=sensitive-cookie-value")
		w.Header().Set("X-Private", "private-header-value")
		_, _ = fmt.Fprintf(w, `{"id":"truncated-1","time":%d,"event":"message","message":"only retained"}`+"\n", t0.Unix())
	}))
	defer upstream.Close()
	complete := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"id":"complete-1","time":%d,"event":"message","message":"complete"}`+"\n", t0.Unix())
	}))
	defer complete.Close()
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `[
			{"id":"local-1","time":%d,"event":"message","message":"first"},
			{"id":"local-2","time":%d,"event":"message","message":"second"}
		]`, t0.Unix(), t0.Add(time.Second).Unix())
	}))
	defer local.Close()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configText := fmt.Sprintf(`version: 1
limits:
  default_window: 15m
  max_incident_window: 1h
  max_log_lines: 1
  source_cache_ttl: 1m
sources:
  ntfy-upstream:
    kind: ntfy
    base_url: %s
  ntfy-complete:
    kind: ntfy
    base_url: %s
  ntfy-local:
    kind: ntfy
    base_url: %s
services:
  - id: upstream
    sources:
      ntfy: { source: ntfy-upstream, topic: truncated }
  - id: complete
    sources:
      ntfy: { source: ntfy-complete, topic: complete }
  - id: local
    sources:
      ntfy: { source: ntfy-local, topic: local }
`, upstream.URL, complete.URL, local.URL)
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}

	binary := stdioTestBinary(t, ctx)
	var stderr bytes.Buffer
	command := exec.CommandContext(ctx, binary, "--config", configPath)
	command.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "ntfy-truncation-fixture", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command, TerminateDuration: stdioShutdownTimeout}, nil)
	if err != nil {
		t.Fatalf("connect to executable: %v\nstderr:\n%s", err, stderr.String())
	}
	defer func() { _ = session.Close() }()

	type bundle struct {
		Items     []json.RawMessage `json:"items"`
		Truncated bool              `json:"truncated"`
		Sources   []struct {
			Kind      string `json:"kind"`
			Truncated bool   `json:"truncated"`
			ItemCount int    `json:"item_count"`
		} `json:"sources"`
	}
	call := func(service string) (bundle, []byte) {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name: "incident_context", Arguments: map[string]any{"service_id": service},
		})
		if err != nil || result.IsError {
			t.Fatalf("%s incident_context failed: %v", service, err)
		}
		raw := mustJSON(t, result.StructuredContent)
		var body bundle
		executableE2EDecode(t, raw, &body)
		return body, raw
	}
	ntfyTruncated := func(body bundle) bool {
		for _, source := range body.Sources {
			if source.Kind == "ntfy" {
				return source.Truncated
			}
		}
		return false
	}

	for attempt := 0; attempt < 2; attempt++ {
		body, raw := call("upstream")
		if len(body.Items) != 1 || !body.Truncated || !ntfyTruncated(body) {
			t.Fatalf("attempt %d lost upstream truncation: %s", attempt, raw)
		}
		for _, secret := range []string{"sensitive-cookie-value", "private-header-value"} {
			if bytes.Contains(raw, []byte(secret)) {
				t.Fatalf("response disclosed %q: %s", secret, raw)
			}
		}
	}
	if upstreamCalls.Load() != 1 {
		t.Fatalf("upstream calls=%d, want=1 from the cache", upstreamCalls.Load())
	}

	completeBody, completeRaw := call("complete")
	if completeBody.Truncated || ntfyTruncated(completeBody) || len(completeBody.Items) != 1 {
		t.Fatalf("complete response reported truncation: %s", completeRaw)
	}

	localBody, localRaw := call("local")
	if !localBody.Truncated || !ntfyTruncated(localBody) || len(localBody.Items) != 1 {
		t.Fatalf("local limit was not reported: %s", localRaw)
	}
}
