package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestNativeReleaseSmoke executes an existing release binary. The release
// workflow runs it on each native runner. It does not rebuild the candidate.
func TestNativeReleaseSmoke(t *testing.T) {
	configured := strings.TrimSpace(os.Getenv("HEM_NATIVE_SMOKE_BINARY"))
	if configured == "" {
		t.Skip("HEM_NATIVE_SMOKE_BINARY is not set")
	}
	expectedVersion := strings.TrimSpace(os.Getenv("HEM_NATIVE_SMOKE_VERSION"))
	if expectedVersion == "" {
		t.Fatal("HEM_NATIVE_SMOKE_VERSION is required")
	}
	source, err := filepath.Abs(configured)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(source)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("candidate binary is not a regular file: %s", source)
	}

	// Exercise a path that contains spaces on every platform.
	dir := filepath.Join(t.TempDir(), "native smoke path")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, filepath.Base(source))
	if err := copyExecutable(source, binary); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runNativeCommand(t, binary, "--version")
	if code != 0 || strings.TrimSpace(stdout) != expectedVersion {
		t.Fatalf("--version code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/endpoints/statuses" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"name": "native", "key": "native",
			"results": []map[string]any{{
				"success": true, "status": 200, "timestamp": time.Now().UTC().Format(time.RFC3339),
			}},
		}})
	}))
	defer upstream.Close()

	configPath := filepath.Join(dir, "config file.yaml")
	configText := fmt.Sprintf(`version: 1
sources:
  gatus:
    kind: gatus
    base_url: %s
services:
  - id: native-service
    sources:
      gatus: { source: gatus, endpoint_key: native }
`, upstream.URL)
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(configPath, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	stdout, stderr, code = runNativeCommand(t, binary, "--config", configPath, "--validate")
	if code != 0 || !strings.Contains(stdout, "config ok") {
		t.Fatalf("valid --validate code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if calls.Load() != 0 {
		t.Fatalf("--validate sent %d source requests", calls.Load())
	}

	invalidConfig := filepath.Join(dir, "invalid config.yaml")
	if err := os.WriteFile(invalidConfig, []byte("version: 99\nsources: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(invalidConfig, 0o600)
	}
	_, _, code = runNativeCommand(t, binary, "--config", invalidConfig, "--validate")
	if code != 1 {
		t.Fatalf("invalid --validate code=%d, want=1", code)
	}

	if runtime.GOOS != "windows" {
		if err := os.Chmod(configPath, 0o644); err != nil {
			t.Fatal(err)
		}
		_, stderr, code = runNativeCommand(t, binary, "--config", configPath, "--validate")
		if code != 1 || !strings.Contains(stderr, "group or world accessible") {
			t.Fatalf("insecure config code=%d stderr=%q", code, stderr)
		}
		if err := os.Chmod(configPath, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var mcpStderr bytes.Buffer
	command := exec.CommandContext(ctx, binary, "--config", configPath)
	command.Env = os.Environ()
	command.Stderr = &mcpStderr
	client := mcp.NewClient(&mcp.Implementation{Name: "native-smoke", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{
		Command:           command,
		TerminateDuration: stdioShutdownTimeout,
	}, nil)
	if err != nil {
		t.Fatalf("MCP initialize: %v\nstderr:\n%s", err, mcpStderr.String())
	}
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 7 {
		t.Fatalf("tools/list: tools=%d err=%v", len(tools.Tools), err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "service_status", Arguments: map[string]any{"service_id": "native-service"},
	})
	if err != nil || result.IsError {
		t.Fatalf("fixture collection: %v", err)
	}
	if !bytes.Contains(mustJSON(t, result.StructuredContent), []byte(`"source":"gatus"`)) {
		t.Fatalf("fixture collection has no gatus evidence: %s", mustJSON(t, result.StructuredContent))
	}
	if err := session.Close(); err != nil {
		t.Fatalf("clean shutdown: %v", err)
	}
	if !strings.Contains(mcpStderr.String(), "starting") {
		t.Fatalf("stderr did not receive diagnostics: %q", mcpStderr.String())
	}
}

func runNativeCommand(t *testing.T, binary string, args ...string) (string, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err == nil {
		return stdout.String(), stderr.String(), 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return stdout.String(), stderr.String(), exitErr.ExitCode()
	}
	t.Fatalf("run %s: %v", filepath.Base(binary), err)
	return "", "", -1
}

func copyExecutable(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
