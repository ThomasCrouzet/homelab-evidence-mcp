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
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// This test detects cross-topic attribution, undated notifications presented
// as live evidence, an empty replay that looks like proven zero history, and a
// full loss of evidence when a valid replay exceeds the HTTP body cap.
func TestStdioNtfyRecordValidityAndCoverage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	t0 := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	handler := func(lines []string) *httptest.Server {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, line := range lines {
				_, _ = fmt.Fprint(w, line)
			}
		}))
		t.Cleanup(server.Close)
		return server
	}
	valid := handler([]string{
		fmt.Sprintf(`{"id":"owned","time":%d,"event":"message","topic":"valid-alerts","message":"owned"}`+"\n", t0.Unix()),
		fmt.Sprintf(`{"id":"foreign","time":%d,"event":"message","topic":"other","message":"foreign"}`+"\n", t0.Unix()),
		fmt.Sprintf(`{"id":"noevent","time":%d,"topic":"valid-alerts","message":"no event"}`+"\n", t0.Unix()),
		fmt.Sprintf(`{"id":"notime","event":"message","topic":"valid-alerts","message":"no time"}` + "\n"),
	})
	empty := handler(nil)
	malformed := handler([]string{
		fmt.Sprintf(`[{"id":"complete","time":%d,"event":"message","topic":"malformed-alerts","message":"complete"}`, t0.Unix()),
	})
	oversized := handler([]string{
		fmt.Sprintf(`{"id":"complete","time":%d,"event":"message","topic":"oversized-alerts","message":"complete"}`+"\n", t0.Unix()),
		fmt.Sprintf(`{"id":"cut","time":%d,"event":"message","topic":"oversized-alerts","message":"%s"}`, t0.Unix(), strings.Repeat("x", 8192)),
	})

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configText := fmt.Sprintf(`version: 1
limits:
  default_window: 15m
  max_incident_window: 1h
  max_body_bytes: 512
sources:
  ntfy-valid:
    kind: ntfy
    base_url: %s
  ntfy-empty:
    kind: ntfy
    base_url: %s
  ntfy-malformed:
    kind: ntfy
    base_url: %s
  ntfy-oversized:
    kind: ntfy
    base_url: %s
services:
  - id: valid
    sources:
      ntfy: { source: ntfy-valid, topic: valid-alerts }
  - id: empty
    sources:
      ntfy: { source: ntfy-empty, topic: empty-alerts }
  - id: malformed
    sources:
      ntfy: { source: ntfy-malformed, topic: malformed-alerts }
  - id: oversized
    sources:
      ntfy: { source: ntfy-oversized, topic: oversized-alerts }
`, valid.URL, empty.URL, malformed.URL, oversized.URL)
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}

	binary := stdioTestBinary(t, ctx)
	var stderr bytes.Buffer
	command := exec.CommandContext(ctx, binary, "--config", configPath)
	command.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "ntfy-coverage", Version: "1"}, nil)
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
		UnknownTime      int    `json:"unknown_time_records"`
		RetentionLimited bool   `json:"retention_limited"`
		LocalTruncated   bool   `json:"local_truncated"`
	}
	type body struct {
		Items []struct {
			SourceID   string         `json:"source_id"`
			Freshness  string         `json:"freshness"`
			Attributes map[string]any `json:"attributes"`
		} `json:"items"`
		Sources []source `json:"sources"`
	}
	call := func(service string) (body, []byte) {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name: "incident_context", Arguments: map[string]any{"service_id": service},
		})
		if err != nil || result.IsError {
			t.Fatalf("%s failed: %v\n%s", service, err, stderr.String())
		}
		raw := mustJSON(t, result.StructuredContent)
		var out body
		executableE2EDecode(t, raw, &out)
		return out, raw
	}
	ntfySource := func(out body) source {
		for _, entry := range out.Sources {
			if entry.Kind == "ntfy" {
				return entry
			}
		}
		t.Fatalf("ntfy source missing: %+v", out.Sources)
		return source{}
	}

	validBody, validRaw := call("valid")
	if len(validBody.Items) != 1 || validBody.Items[0].SourceID != "owned" {
		t.Fatalf("cross-topic or invalid record became evidence: %s", validRaw)
	}
	if got := ntfySource(validBody); got.Status != "ok" || got.Reason != "partial_invalid_records" ||
		got.Complete || !got.RetentionLimited || got.Discarded != 3 || got.UnknownTime != 1 {
		t.Fatalf("valid replay coverage=%+v", got)
	}

	emptyBody, emptyRaw := call("empty")
	if len(emptyBody.Items) != 0 {
		t.Fatalf("empty replay produced items: %s", emptyRaw)
	}
	if got := ntfySource(emptyBody); got.Reason != "retention_limited" || got.Complete || !got.RetentionLimited {
		t.Fatalf("empty replay claimed complete history: %+v", got)
	}

	malformedBody, malformedRaw := call("malformed")
	if len(malformedBody.Items) != 1 {
		t.Fatalf("malformed array lost complete evidence: %s", malformedRaw)
	}
	if got := ntfySource(malformedBody); !got.LocalTruncated || got.Complete || got.Discarded != 1 {
		t.Fatalf("malformed array coverage=%+v", got)
	}

	oversizedBody, oversizedRaw := call("oversized")
	if len(oversizedBody.Items) != 1 || oversizedBody.Items[0].SourceID != "complete" {
		t.Fatalf("oversized replay lost complete evidence: %s", oversizedRaw)
	}
	got := ntfySource(oversizedBody)
	if !got.LocalTruncated || got.Complete || !got.RetentionLimited {
		t.Fatalf("oversized replay coverage=%+v", got)
	}
	var parsed map[string]any
	if err := json.Unmarshal(oversizedRaw, &parsed); err != nil {
		t.Fatal(err)
	}
}
