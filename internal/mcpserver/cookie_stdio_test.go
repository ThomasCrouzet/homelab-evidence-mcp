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

// This test detects partial Cookie and Set-Cookie redaction in live and cached
// evidence. The fixture uses synthetic cookie values only.
func TestStdioRedactsCompleteCookieHeaders(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	base := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	lines := []string{
		"request headers: Cookie: sid=alpha; refresh=bravo",
		"response headers: sEt-CoOkIe: sid=charlie; refresh=delta",
		`quoted header: Cookie: sid="echo foxtrot"; refresh="golf hotel"`,
	}
	secrets := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel"}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		values := make([][]string, 0, len(lines))
		for index, line := range lines {
			values = append(values, []string{
				strconv.FormatInt(base.Add(time.Duration(index)*time.Second).UnixNano(), 10),
				line,
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"resultType": "streams",
				"result": []map[string]any{{
					"stream": map[string]string{"container": "cookie-fixture"},
					"values": values,
				}},
			},
		})
	}))
	defer upstream.Close()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configText := fmt.Sprintf(`version: 1
limits:
  max_log_lines: 10
sources:
  loki:
    kind: loki
    base_url: %s
services:
  - id: cookie-service
    sources:
      loki:
        source: loki
        selector: '{container="cookie-fixture"}'
`, upstream.URL)
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}

	binary := stdioTestBinary(t, ctx)
	var stderr bytes.Buffer
	command := exec.CommandContext(ctx, binary, "--config", configPath)
	command.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "cookie-fixture", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command, TerminateDuration: stdioShutdownTimeout}, nil)
	if err != nil {
		t.Fatalf("connect to executable: %v\nstderr:\n%s", err, stderr.String())
	}
	defer func() { _ = session.Close() }()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "search_logs",
		Arguments: map[string]any{"service_id": "cookie-service", "duration": "1h", "limit": 10},
	})
	if err != nil || result.IsError {
		t.Fatalf("search_logs failed: %v\n%s", err, stderr.String())
	}
	assertCookieSecretsAbsent(t, "search_logs", result, secrets)

	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Items []struct {
			ID                string `json:"id"`
			Summary           string `json:"summary"`
			RedactionsApplied int    `json:"redactions_applied"`
			Attributes        struct {
				Line string `json:"line"`
			} `json:"attributes"`
		} `json:"items"`
	}
	if err := json.Unmarshal(encoded, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != len(lines) {
		t.Fatalf("items=%d, want=%d: %s", len(response.Items), len(lines), encoded)
	}
	for _, item := range response.Items {
		if item.RedactionsApplied != 1 {
			t.Fatalf("redactions_applied=%d, want=1: %+v", item.RedactionsApplied, item)
		}
		if !bytes.Contains([]byte(item.Attributes.Line), []byte("[REDACTED]")) ||
			!bytes.Contains([]byte(item.Summary), []byte("[REDACTED]")) {
			t.Fatalf("redaction marker missing from line or summary: %+v", item)
		}
	}

	for _, item := range response.Items {
		cached, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      "get_evidence",
			Arguments: map[string]any{"id": item.ID},
		})
		if err != nil || cached.IsError {
			t.Fatalf("get_evidence failed: %v", err)
		}
		assertCookieSecretsAbsent(t, "get_evidence", cached, secrets)
	}
}

func assertCookieSecretsAbsent(t *testing.T, tool string, result *mcp.CallToolResult, secrets []string) {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range secrets {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("%s leaked cookie value %q: %s", tool, secret, encoded)
		}
	}
}
