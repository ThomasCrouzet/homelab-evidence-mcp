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
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type integration182StatusView struct {
	Items []struct {
		ObservedAt time.Time `json:"observed_at"`
	} `json:"items"`
	Sources []struct {
		Kind   string `json:"kind"`
		Status string `json:"status"`
	} `json:"sources"`
}

func integration182GatusBody(key, padding string) string {
	return fmt.Sprintf(
		`[{"name":"endpoint","group":"integration","key":%q,"description":%q,"results":[{"success":true,"status":200,"timestamp":%q}]}]`,
		key, padding, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339),
	)
}

func integration182GatusServer(t *testing.T, body string, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/endpoints/statuses" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func integration182Session(t *testing.T, ctx context.Context, binary, configText string) *mcp.ClientSession {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command := exec.CommandContext(ctx, binary, "--config", configPath)
	command.Env = os.Environ()
	command.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "integration-182", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command, TerminateDuration: stdioShutdownTimeout}, nil)
	if err != nil {
		t.Fatalf("connect to executable: %v\nstderr:\n%s", err, stderr.String())
	}
	t.Cleanup(func() {
		// The test context can be canceled before this cleanup runs, so the
		// child shutdown signal is not part of the behavior under test.
		_ = session.Close()
	})
	return session
}

func integration182SessionCapabilities(t *testing.T, ctx context.Context, session *mcp.ClientSession) (integration182CapabilitiesView, []byte) {
	t.Helper()
	raw := executableE2ECall(t, ctx, session, "evidence_capabilities", map[string]any{})
	var out integration182CapabilitiesView
	executableE2EDecode(t, raw, &out)
	return out, raw
}

// This test detects a shared source byte budget that is not enforced across
// plain and CIDR-isolated clients. It uses the real executable and local
// fixture servers.
func TestStdioSourceCacheByteBudget(t *testing.T) {
	const capBytes int64 = 65536
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	binary := stdioTestBinary(t, ctx)

	padding := strings.Repeat("p", 41000)
	plainBody := integration182GatusBody("plain-app", padding)
	isolatedBody := integration182GatusBody("isolated-app", padding)
	if len(plainBody) < 40<<10 || len(isolatedBody) < 40<<10 {
		t.Fatalf("fixture bodies are too small: plain=%d isolated=%d", len(plainBody), len(isolatedBody))
	}
	var plainCalls atomic.Int32
	var isolatedCalls atomic.Int32
	plainServer := integration182GatusServer(t, plainBody, &plainCalls)
	isolatedServer := integration182GatusServer(t, isolatedBody, &isolatedCalls)

	configText := fmt.Sprintf(`version: 1
limits:
  source_cache_ttl: 5m
  source_cache_max_bytes: %d
  per_source_timeout: 3s
  total_timeout: 15s
sources:
  plain:
    kind: gatus
    base_url: %s
  isolated:
    kind: gatus
    base_url: %s
    allowed_cidrs: ["127.0.0.0/8"]
services:
  - id: plain-service
    sources:
      gatus: { source: plain, endpoint_key: plain-app }
  - id: isolated-service
    sources:
      gatus: { source: isolated, endpoint_key: isolated-app }
`, capBytes, plainServer.URL, isolatedServer.URL)
	session := integration182Session(t, ctx, binary, configText)

	for _, serviceID := range []string{"plain-service", "isolated-service"} {
		raw := executableE2ECall(t, ctx, session, "service_status", map[string]any{"service_id": serviceID})
		var out integration182StatusView
		executableE2EDecode(t, raw, &out)
		if len(out.Items) == 0 {
			t.Fatalf("service_status %s returned no items: %s", serviceID, raw)
		}
		ok := false
		for _, source := range out.Sources {
			if source.Kind == "gatus" && source.Status == "ok" {
				ok = true
			}
		}
		if !ok {
			t.Fatalf("service_status %s did not report an ok Gatus source: %s", serviceID, raw)
		}
	}
	if plainCalls.Load() != 1 || isolatedCalls.Load() != 1 {
		t.Fatalf("upstream calls plain=%d isolated=%d, want one each", plainCalls.Load(), isolatedCalls.Load())
	}

	caps, raw := integration182SessionCapabilities(t, ctx, session)
	if caps.SourceCache.MaxBytes != capBytes {
		t.Fatalf("source_cache.max_bytes=%d, want %d", caps.SourceCache.MaxBytes, capBytes)
	}
	if caps.SourceCache.Bytes <= 0 || caps.SourceCache.Bytes > capBytes {
		t.Fatalf("source_cache.bytes=%d, cap=%d", caps.SourceCache.Bytes, capBytes)
	}
	if caps.SourceCache.Bytes != int64(len(isolatedBody)) {
		t.Fatalf("source_cache.bytes=%d, want the retained body %d", caps.SourceCache.Bytes, len(isolatedBody))
	}
	if caps.SourceCache.Size != 1 {
		t.Fatalf("source_cache.size=%d, want one retained entry after the second insert", caps.SourceCache.Size)
	}
	if got, ok := caps.Limits["source_cache_max_bytes"].(float64); !ok || got != float64(capBytes) {
		t.Fatalf("limits.source_cache_max_bytes=%v, want %d", caps.Limits["source_cache_max_bytes"], capBytes)
	}
	if bytes.Contains(raw, []byte("pppppppp")) {
		t.Fatal("capabilities output contains cached response body text")
	}
}
