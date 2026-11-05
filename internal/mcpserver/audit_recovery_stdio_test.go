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

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/audit"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/version"
)

// This test detects an audit writer that stays closed after a real rotation
// failure, partial records, and repeated or missing diagnostics.
func TestStdioAuditRecoversAfterRotationFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer upstream.Close()

	now := time.Now().UTC()
	seed := []byte(`{"action":"seed","status":"ok"}` + "\n")
	boot, err := json.Marshal(audit.Event{
		Time: now, Action: "boot", Status: "ok",
		Detail: fmt.Sprintf("version=%s services=0", version.Version),
	})
	if err != nil {
		t.Fatal(err)
	}
	toolEvent, err := json.Marshal(audit.Event{
		Time: now, Action: "tool", Tool: "list_services", Status: "ok", Detail: "total=0",
	})
	if err != nil {
		t.Fatal(err)
	}
	// The boot record fits. One tool record pushes the file over the limit.
	maxBytes := int64(len(seed) + len(boot) + 1 + len(toolEvent)/2)

	dir := t.TempDir()
	auditPath := filepath.Join(dir, "audit.jsonl")
	if err := os.WriteFile(auditPath, seed, 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.yaml")
	configText := fmt.Sprintf(`version: 1
audit:
  file: %s
  max_bytes: %d
  max_files: 2
sources:
  fixture:
    kind: gatus
    base_url: %s
services: []
`, auditPath, maxBytes, upstream.URL)
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	command := exec.CommandContext(ctx, stdioTestBinary(t, ctx), "--config", configPath)
	command.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "audit-recovery-e2e", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{
		Command: command, TerminateDuration: stdioShutdownTimeout,
	}, nil)
	if err != nil {
		t.Fatalf("connect to executable: %v\nstderr:\n%s", err, stderr.String())
	}
	defer func() { _ = session.Close() }()

	obstruct := func() {
		t.Helper()
		backup := auditPath + ".1"
		if err := os.RemoveAll(backup); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(backup, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(backup, "keep"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	clear := func() {
		t.Helper()
		if err := os.RemoveAll(auditPath + ".1"); err != nil {
			t.Fatal(err)
		}
	}
	call := func() {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_services", Arguments: map[string]any{}})
		if err != nil || result == nil || result.IsError {
			t.Fatalf("MCP call failed during audit storage failure: %v", err)
		}
	}
	diagnostics := func() int {
		return strings.Count(stderr.String(), "audit write failed\n")
	}

	obstruct()
	call()
	call()
	if got := diagnostics(); got != 1 {
		t.Fatalf("continuous failure produced %d diagnostics, want 1:\n%s", got, stderr.String())
	}
	clear()
	call()

	obstruct()
	call()
	if got := diagnostics(); got != 2 {
		t.Fatalf("a later independent failure produced %d diagnostics, want 2:\n%s", got, stderr.String())
	}
	clear()
	call()
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{auditPath, auditPath + ".1"} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if len(content) == 0 {
			t.Fatalf("%s is empty", path)
		}
		for _, line := range bytes.Split(bytes.TrimSpace(content), []byte("\n")) {
			var event audit.Event
			if err := json.Unmarshal(line, &event); err != nil {
				t.Fatalf("%s contains a partial record: %v\n%s", path, err, content)
			}
		}
	}
	primary, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(primary, []byte(`"tool":"list_services"`)) {
		t.Fatalf("no complete audit event after recovery: %s", primary)
	}
	st, err := os.Stat(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("reopened audit file mode=%o", st.Mode().Perm())
	}
	if strings.Contains(stderr.String(), dir) {
		t.Fatal("audit diagnostics disclosed the storage path")
	}
}
