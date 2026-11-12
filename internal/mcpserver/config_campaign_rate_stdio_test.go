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
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestStdioEffectiveSourceRequestLimit detects a resolved limit of 0 in
// evidence_capabilities while the adapters apply their own default of 100.
func TestStdioEffectiveSourceRequestLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer upstream.Close()

	binary := stdioTestBinary(t, ctx)
	cases := []struct {
		name  string
		limit string
		want  int
	}{
		{name: "omitted", limit: "", want: 100},
		{name: "explicit zero", limit: "  max_source_requests_per_minute: 0\n", want: 100},
		{name: "explicit seven", limit: "  max_source_requests_per_minute: 7\n", want: 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			configText := fmt.Sprintf(`version: 1
limits:
%s  source_cache_ttl: 0s
sources:
  gatus:
    kind: gatus
    base_url: %s
services:
  - id: api
    sources:
      gatus: { source: gatus, endpoint_key: api }
`, tc.limit, upstream.URL)
			if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
				t.Fatal(err)
			}

			var stderr bytes.Buffer
			command := exec.CommandContext(ctx, binary, "--config", configPath)
			command.Stderr = &stderr
			client := mcp.NewClient(&mcp.Implementation{Name: "config-rate", Version: "1"}, nil)
			session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command, TerminateDuration: stdioShutdownTimeout}, nil)
			if err != nil {
				t.Fatalf("connect: %v\n%s", err, stderr.String())
			}
			defer func() { _ = session.Close() }()

			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "evidence_capabilities"})
			if err != nil || result.IsError {
				t.Fatalf("evidence_capabilities failed: %v\n%s", err, stderr.String())
			}
			var body struct {
				Limits struct {
					MaxSourceRequestsPerMinute int `json:"max_source_requests_per_minute"`
				} `json:"limits"`
			}
			executableE2EDecode(t, mustJSON(t, result.StructuredContent), &body)
			if body.Limits.MaxSourceRequestsPerMinute != tc.want {
				t.Fatalf("reported limit=%d, want %d: %s",
					body.Limits.MaxSourceRequestsPerMinute, tc.want, mustJSON(t, result.StructuredContent))
			}
		})
	}
}

// TestStdioValidateSourceRequestRateRange detects --validate that accepts a
// negative or oversized source request rate.
func TestStdioValidateSourceRequestRateRange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	binary := stdioTestBinary(t, ctx)
	for _, limit := range []string{"-1", "1001"} {
		t.Run(limit, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			configText := fmt.Sprintf(`version: 1
limits:
  max_source_requests_per_minute: %s
sources:
  gatus:
    kind: gatus
    base_url: https://gatus.example.internal
services:
  - id: api
    sources:
      gatus: { source: gatus, endpoint_key: api }
`, limit)
			if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
				t.Fatal(err)
			}
			_, stderr, code := runNativeCommand(t, binary, "--config", configPath, "--validate")
			if code != 2 {
				t.Fatalf("--validate exit=%d, want 2: %s", code, stderr)
			}
			if !strings.Contains(stderr, "limits.max_source_requests_per_minute") {
				t.Fatalf("--validate error is not field-specific: %s", stderr)
			}
		})
	}
}
