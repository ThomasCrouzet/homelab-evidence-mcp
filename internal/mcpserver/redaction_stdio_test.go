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

func TestStdioRedactionRules(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	binary := stdioTestBinary(t, ctx)

	tests := []struct {
		name        string
		line        string
		rules       []string
		wantLine    string
		wantRegions int
	}{
		{
			name:        "escaped JSON credential",
			line:        `{"password":"prefix\"remaining-secret"}`,
			wantLine:    `{[REDACTED]}`,
			wantRegions: 1,
		},
		{
			name:        "overlapping rules",
			line:        "secret-value-tail",
			rules:       []string{`secret-value`, `value-tail`},
			wantLine:    "[REDACTED]",
			wantRegions: 1,
		},
		{
			name:        "replacement marker isolation",
			line:        "ab",
			rules:       []string{`.`, `.`},
			wantLine:    "[REDACTED]",
			wantRegions: 1,
		},
		{
			name:        "zero-length matches",
			line:        "safe text",
			rules:       []string{`^`, `\b`},
			wantLine:    "safe text",
			wantRegions: 0,
		},
		{
			name:        "start anchor",
			line:        "foofoo",
			rules:       []string{`^foo`},
			wantLine:    "[REDACTED]foo",
			wantRegions: 1,
		},
		{
			name:        "word boundary",
			line:        "xsecret",
			rules:       []string{`x|\bsecret`},
			wantLine:    "[REDACTED]secret",
			wantRegions: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("method=%s", r.Method)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"status": "success",
					"data": map[string]any{
						"resultType": "streams",
						"result": []map[string]any{{
							"stream": map[string]string{},
							"values": [][]string{{fmt.Sprint(time.Now().UTC().Add(-time.Minute).UnixNano()), test.line}},
						}},
					},
				})
			}))
			defer upstream.Close()

			var redactConfig strings.Builder
			if len(test.rules) > 0 {
				redactConfig.WriteString("redact:\n")
				for _, rule := range test.rules {
					_, _ = fmt.Fprintf(&redactConfig, "  - regex: %q\n", rule)
				}
			}
			dir := t.TempDir()
			configPath := filepath.Join(dir, "config.yaml")
			configText := fmt.Sprintf(`version: 1
sources:
  loki:
    kind: loki
    base_url: %s
services:
  - id: fixture
    sources:
      loki:
        source: loki
        selector: '{service="fixture"}'
%s`, upstream.URL, redactConfig.String())
			if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
				t.Fatal(err)
			}

			command := exec.CommandContext(ctx, binary, "--config", configPath)
			var stderr bytes.Buffer
			command.Stderr = &stderr
			client := mcp.NewClient(&mcp.Implementation{Name: "redaction-fixture", Version: "1"}, nil)
			session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command, TerminateDuration: stdioShutdownTimeout}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = session.Close() }()

			result, err := session.CallTool(ctx, &mcp.CallToolParams{
				Name: "search_logs",
				Arguments: map[string]any{
					"service_id": "fixture",
					"duration":   "1h",
				},
			})
			if err != nil || result.IsError {
				t.Fatalf("search_logs failed: %v stderr=%s", err, stderr.String())
			}
			body, err := json.Marshal(result.StructuredContent)
			if err != nil {
				t.Fatal(err)
			}
			var response struct {
				Items []struct {
					Attributes struct {
						Line string `json:"line"`
					} `json:"attributes"`
					RedactionsApplied int `json:"redactions_applied"`
				} `json:"items"`
			}
			if err := json.Unmarshal(body, &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Items) != 1 {
				t.Fatalf("items=%d response=%s", len(response.Items), body)
			}
			item := response.Items[0]
			if item.Attributes.Line != test.wantLine || item.RedactionsApplied != test.wantRegions {
				t.Fatalf(
					"line=%q regions=%d, want line=%q regions=%d",
					item.Attributes.Line, item.RedactionsApplied, test.wantLine, test.wantRegions,
				)
			}
			t.Logf("line=%q redacted_regions=%d", item.Attributes.Line, item.RedactionsApplied)
		})
	}
}
