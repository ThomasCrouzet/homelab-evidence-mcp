package mcpserver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestStdioValidateLokiSelectorRules detects --validate that accepts
// duplicate equality labels or rejects a supported two-label selector.
func TestStdioValidateLokiSelectorRules(t *testing.T) {
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

	for _, selector := range []string{
		`{container="frontend",container="backend"}`,
		`{container="frontend",container="frontend"}`,
		`{app="one",container="two",app="three"}`,
	} {
		t.Run(selector, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(configPath, []byte(lokiConfig(selector)), 0o600); err != nil {
				t.Fatal(err)
			}
			_, stderr, code := runNativeCommand(t, binary, "--config", configPath, "--validate")
			if code != 2 {
				t.Fatalf("--validate exit=%d, want 2: %s", code, stderr)
			}
			if !strings.Contains(stderr, "services[0].sources.loki.selector") {
				t.Fatalf("--validate error does not name the selector field: %s", stderr)
			}
			for _, leaked := range []string{"frontend", "backend", "container", "app", "one", "two", "three"} {
				if strings.Contains(stderr, leaked) {
					t.Fatalf("--validate error exposes selector content %q: %s", leaked, stderr)
				}
			}
		})
	}

	validPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(validPath, []byte(lokiConfig(`{app="x",host="y"} |= "error"`)), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runNativeCommand(t, binary, "--config", validPath, "--validate")
	if code != 0 || !strings.Contains(stdout, "config ok") {
		t.Fatalf("two-label selector rejected: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}
