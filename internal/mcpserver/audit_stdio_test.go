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

// This test detects audit storage failures that stop healthy MCP tool calls.
// It also detects diagnostics that expose storage details or repeat without recovery.
func TestStdioAuditFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	dir := t.TempDir()
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer upstream.Close()
	auditPath := filepath.Join(dir, "audit-sensitive-location.jsonl")
	configPath := filepath.Join(dir, "config.yaml")
	configText := fmt.Sprintf(`version: 1
audit:
  file: %s
  max_bytes: 1
  max_files: 2
sources:
  fixture:
    kind: gatus
    base_url: %s
services: []
`, auditPath, upstream.URL)
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	command := exec.CommandContext(ctx, stdioTestBinary(t, ctx), "--config", configPath)
	command.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "audit-e2e", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{
		Command: command, TerminateDuration: stdioShutdownTimeout,
	}, nil)
	if err != nil {
		t.Fatalf("connect to executable: %v\nstderr:\n%s", err, stderr.String())
	}

	capabilities := executableE2ECall(t, ctx, session, "evidence_capabilities", map[string]any{})
	if !bytes.Contains(capabilities, []byte(`"readonly": true`)) {
		t.Fatalf("capabilities call did not succeed: %s", capabilities)
	}
	services := executableE2ECall(t, ctx, session, "list_services", map[string]any{})
	if !bytes.Contains(services, []byte(`"total": 0`)) {
		t.Fatalf("list_services call did not succeed: %s", services)
	}
	_ = session.Close()
	if got := upstreamCalls.Load(); got != 0 {
		t.Fatalf("startup and registry tools sent %d upstream requests", got)
	}

	content, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 0 {
		t.Fatalf("rejected audit records changed the file: %q", content)
	}
	diagnostics := stderr.String()
	if strings.Count(diagnostics, "audit write failed\n") != 1 {
		t.Fatalf("unexpected audit diagnostic count:\n%s", diagnostics)
	}
	for _, forbidden := range []string{"audit-sensitive-location", "max_bytes", "exceeds"} {
		if strings.Contains(diagnostics, forbidden) {
			t.Fatalf("audit diagnostic contains %q:\n%s", forbidden, diagnostics)
		}
	}
}
