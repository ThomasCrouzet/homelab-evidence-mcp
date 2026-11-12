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
)

// TestStdioValidateLokiSelectorDelimiters detects --validate that accepts a
// duplicate equality key hidden behind a brace inside an earlier quoted value.
func TestStdioValidateLokiSelectorDelimiters(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	binary := stdioTestBinary(t, ctx)
	lokiConfig := func(selector string) string {
		return fmt.Sprintf(`version: 1
sources:
  loki:
    kind: loki
    base_url: https://loki.example.internal
services:
  - id: svc
    sources:
      loki: { source: loki, selector: '%s' }
`, selector)
	}

	rejected := []struct {
		name     string
		selector string
		leaks    []string
	}{
		{
			name:     "conflicting duplicate after quoted brace",
			selector: `{container="frontend",note="x}y",container="backend"}`,
			leaks:    []string{"frontend", "backend", "x}y"},
		},
		{
			name:     "equal duplicate after quoted brace",
			selector: `{container="frontend",note="x}y",container="frontend"}`,
			leaks:    []string{"frontend", "x}y"},
		},
		{
			name:     "duplicate after escaped quote",
			selector: `{container="frontend",note="x\"}y",container="backend"}`,
			leaks:    []string{"frontend", "backend"},
		},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(configPath, []byte(lokiConfig(tc.selector)), 0o600); err != nil {
				t.Fatal(err)
			}
			_, stderr, code := runNativeCommand(t, binary, "--config", configPath, "--validate")
			if code != 2 {
				t.Fatalf("--validate exit=%d, want 2: %s", code, stderr)
			}
			if !strings.Contains(stderr, "services[0].sources.loki.selector") {
				t.Fatalf("--validate error does not name the selector field: %s", stderr)
			}
			for _, leaked := range tc.leaks {
				if strings.Contains(stderr, leaked) {
					t.Fatalf("--validate error exposes selector content %q: %s", leaked, stderr)
				}
			}
		})
	}

	validPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(validPath, []byte(lokiConfig(`{container="frontend",note="x}y"} |= "error"`)), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runNativeCommand(t, binary, "--config", validPath, "--validate")
	if code != 0 || !strings.Contains(stdout, "config ok") {
		t.Fatalf("quoted brace with distinct labels rejected: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

// TestStdioHiddenDuplicateSendsNoRequest detects a hidden duplicate selector
// that starts the server and attributes a Loki stream to the service.
func TestStdioHiddenDuplicateSendsNoRequest(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var calls atomic.Int32
	loki := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"streams","result":[]}}`))
	}))
	defer loki.Close()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configText := fmt.Sprintf(`version: 1
sources:
  loki:
    kind: loki
    base_url: %s
services:
  - id: api
    sources:
      loki: { source: loki, selector: '{container="frontend",note="x}y",container="backend"}' }
`, loki.URL)
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}

	binary := stdioTestBinary(t, ctx)
	var stdout, stderr bytes.Buffer
	command := exec.CommandContext(ctx, binary, "--config", configPath)
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err == nil {
		t.Fatalf("server started with a hidden duplicate selector: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "services[0].sources.loki.selector") {
		t.Fatalf("startup error does not name the selector field: %s", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("server wrote protocol output before rejecting the config: %q", stdout.String())
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid selector sent %d upstream request(s)", calls.Load())
	}
}
