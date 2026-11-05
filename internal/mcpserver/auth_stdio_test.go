package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// This test detects an authentication header that a static header can
// overwrite, and validation that runs after an HTTP request.
func TestStdioAuthHeaderCollision(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	const (
		credential = "fixture-auth-credential-larkspur"
		static     = "fixture-static-value"
	)
	var calls atomic.Int32
	var bad atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		values := r.Header.Values("X-Access")
		if len(values) != 1 || values[0] != credential {
			bad.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("X-Tenant") != "media" {
			bad.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"name": "api", "key": "api",
			"results": []map[string]any{{
				"success": true, "status": 200, "timestamp": time.Now().UTC().Format(time.RFC3339),
			}},
		}})
	}))
	defer upstream.Close()

	validConfig := filepath.Join(t.TempDir(), "valid.yaml")
	validText := fmt.Sprintf(`version: 1
limits:
  source_cache_ttl: 0s
sources:
  gatus:
    kind: gatus
    base_url: %s
    token_env: HEM_AUTH_TOKEN
    token_header: X-Access
    headers:
      X-Tenant: media
services:
  - id: auth-service
    sources:
      gatus: { source: gatus, endpoint_key: api }
`, upstream.URL)
	if err := os.WriteFile(validConfig, []byte(validText), 0o600); err != nil {
		t.Fatal(err)
	}

	binary := stdioTestBinary(t, ctx)
	var stderr bytes.Buffer
	command := exec.CommandContext(ctx, binary, "--config", validConfig)
	command.Env = append(os.Environ(), "HEM_AUTH_TOKEN="+credential)
	command.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "auth-fixture", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command, TerminateDuration: stdioShutdownTimeout}, nil)
	if err != nil {
		t.Fatalf("connect to executable: %v\nstderr:\n%s", err, stderr.String())
	}
	for i := 0; i < 20; i++ {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name: "service_status", Arguments: map[string]any{"service_id": "auth-service"},
		})
		if err != nil || result.IsError {
			t.Fatalf("call %d failed: %v\n%s", i, err, stderr.String())
		}
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 20 || bad.Load() != 0 {
		t.Fatalf("upstream calls=%d bad=%d", calls.Load(), bad.Load())
	}
	if bytes.Contains(stderr.Bytes(), []byte(static)) || bytes.Contains(stderr.Bytes(), []byte(credential)) {
		t.Fatal("stderr disclosed a header value")
	}

	beforeValidate := calls.Load()
	for _, spelling := range []string{"x-access", "X-ACCESS", "X-Access"} {
		dir := t.TempDir()
		badConfig := filepath.Join(dir, "invalid.yaml")
		badText := fmt.Sprintf(`version: 1
sources:
  gatus:
    kind: gatus
    base_url: %s
    token_env: HEM_AUTH_TOKEN
    token_header: X-Access
    headers:
      %s: %s
services: []
`, upstream.URL, spelling, static)
		if err := os.WriteFile(badConfig, []byte(badText), 0o600); err != nil {
			t.Fatal(err)
		}
		command := exec.CommandContext(ctx, binary, "--validate", "--config", badConfig)
		command.Env = append(os.Environ(), "HEM_AUTH_TOKEN="+credential)
		var validateStderr bytes.Buffer
		command.Stderr = &validateStderr
		err := command.Run()
		var exitErr *exec.ExitError
		if err == nil || !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			t.Fatalf("%s: err=%v, want exit code 1\n%s", spelling, err, validateStderr.String())
		}
		message := validateStderr.String()
		if !bytes.Contains([]byte(message), []byte("conflicts")) ||
			!bytes.Contains([]byte(message), []byte("X-Access")) {
			t.Fatalf("%s: error does not identify the header: %s", spelling, message)
		}
		if bytes.Contains([]byte(message), []byte(static)) || bytes.Contains([]byte(message), []byte(credential)) {
			t.Fatalf("%s: error disclosed a header value: %s", spelling, message)
		}
	}
	if calls.Load() != beforeValidate {
		t.Fatalf("--validate sent %d HTTP requests", calls.Load()-beforeValidate)
	}
}
