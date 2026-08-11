package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParse_ValidMinimal(t *testing.T) {
	raw := []byte(`
version: 1
sources:
  gatus:
    kind: gatus
    base_url: https://gatus.example.internal
services:
  - id: media
    display_name: Media
    sources:
      gatus:
        source: gatus
        endpoint_key: media_app
`)
	cfg, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Limits.DefaultWindow != time.Hour {
		t.Fatalf("default window: %v", cfg.Limits.DefaultWindow)
	}
	if len(cfg.Services) != 1 || cfg.Services[0].ID != "media" {
		t.Fatalf("services: %+v", cfg.Services)
	}
}

func TestParse_RejectsTemplate(t *testing.T) {
	_, err := Parse([]byte("version: 1\nsources: {{ . }}"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParse_RejectsBadServiceID(t *testing.T) {
	raw := []byte(`
version: 1
sources:
  gatus:
    kind: gatus
    base_url: https://gatus.example.internal
services:
  - id: "BAD ID"
    sources:
      gatus:
        source: gatus
        endpoint_key: x
`)
	_, err := Parse(raw)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParse_RejectsUnknownSourceRef(t *testing.T) {
	raw := []byte(`
version: 1
sources:
  gatus:
    kind: gatus
    base_url: https://gatus.example.internal
services:
  - id: media
    sources:
      gatus:
        source: missing
        endpoint_key: x
`)
	_, err := Parse(raw)
	if err == nil || !strings.Contains(err.Error(), "unknown source") {
		t.Fatalf("got %v", err)
	}
}

func TestParse_RejectsSecretInURL(t *testing.T) {
	raw := []byte(`
version: 1
sources:
  hc:
    kind: healthchecks
    base_url: https://hc.example.internal?token=supersecretvalue
    token_env: HC_TOKEN
services: []
`)
	_, err := Parse(raw)
	if err == nil || !strings.Contains(err.Error(), "query strings are not allowed") {
		t.Fatalf("got %v", err)
	}
}

func TestParse_RejectsUnsafeBasePath(t *testing.T) {
	raw := []byte(`
version: 1
sources:
  gatus:
    kind: gatus
    base_url: https://gatus.example.internal/api/%2e%2e/admin
`)
	_, err := Parse(raw)
	if err == nil || !strings.Contains(err.Error(), "dot segment") {
		t.Fatalf("got %v", err)
	}
}

func TestParse_RejectsAuthHeaderLiteral(t *testing.T) {
	raw := []byte(`
version: 1
sources:
  gatus:
    kind: gatus
    base_url: https://gatus.example.internal
    headers:
      Authorization: Bearer abcdefghijklmnopqrstuvwxyz
services: []
`)
	_, err := Parse(raw)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParse_RejectsUnknownField(t *testing.T) {
	raw := []byte(`
version: 1
sources:
  gatus:
    kind: gatus
    base_url: https://gatus.example.internal
    base_urll: https://typo.example.internal
`)
	_, err := Parse(raw)
	if err == nil || !strings.Contains(err.Error(), "field base_urll not found") {
		t.Fatalf("got %v", err)
	}
}

func TestParse_DoesNotLeakInvalidScalarInError(t *testing.T) {
	const secret = "not-a-real-secret-value"
	raw := []byte(`
version: 1
limits:
  max_log_lines: ` + secret + `
sources:
  gatus:
    kind: gatus
    base_url: https://gatus.example.internal
`)
	_, err := Parse(raw)
	if err == nil {
		t.Fatal("expected a type error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("value leaked: %v", err)
	}
}

func TestParse_RejectsMultipleDocuments(t *testing.T) {
	raw := []byte(`
version: 1
sources:
  gatus:
    kind: gatus
    base_url: https://gatus.example.internal
---
version: 1
sources:
  other:
    kind: gatus
    base_url: https://other.example.internal
`)
	_, err := Parse(raw)
	if err == nil || !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("got %v", err)
	}
}

func TestParse_RejectsSensitiveStaticHeader(t *testing.T) {
	raw := []byte(`
version: 1
sources:
  gatus:
    kind: gatus
    base_url: https://gatus.example.internal
    headers:
      X-Auth-Token: short
`)
	_, err := Parse(raw)
	if err == nil || !strings.Contains(err.Error(), "authentication headers") {
		t.Fatalf("got %v", err)
	}
}

func TestParse_RejectsReservedHeader(t *testing.T) {
	raw := []byte(`
version: 1
sources:
  gatus:
    kind: gatus
    base_url: https://gatus.example.internal
    headers:
      Connection: keep-alive
`)
	_, err := Parse(raw)
	if err == nil || !strings.Contains(err.Error(), "reserved header name") {
		t.Fatalf("got %v", err)
	}
}

func TestParse_RejectsCaseInsensitiveDuplicateHeaders(t *testing.T) {
	raw := []byte(`
version: 1
sources:
  gatus:
    kind: gatus
    base_url: https://gatus.example.internal
    headers:
      X-Scope: first
      x-scope: second
`)
	_, err := Parse(raw)
	if err == nil || !strings.Contains(err.Error(), "duplicate header name") {
		t.Fatalf("got %v", err)
	}
}

func TestParse_RejectsInvalidTokenEnv(t *testing.T) {
	raw := []byte(`
version: 1
sources:
  gatus:
    kind: gatus
    base_url: https://gatus.example.internal
    token_env: "BAD NAME"
`)
	_, err := Parse(raw)
	if err == nil || !strings.Contains(err.Error(), "environment variable name") {
		t.Fatalf("got %v", err)
	}
}

func TestParse_TokenHeaderRequiresTokenSource(t *testing.T) {
	raw := []byte(`
version: 1
sources:
  gatus:
    kind: gatus
    base_url: https://gatus.example.internal
    token_header: X-Custom-Auth
`)
	_, err := Parse(raw)
	if err == nil || !strings.Contains(err.Error(), "requires token_env or token_file") {
		t.Fatalf("got %v", err)
	}
}

func TestParse_LokiSelector(t *testing.T) {
	good := []byte(`
version: 1
sources:
  loki:
    kind: loki
    base_url: https://loki.example.internal
services:
  - id: app
    sources:
      loki:
        source: loki
        selector: '{app="x",host="y"} |= "error"'
`)
	if _, err := Parse(good); err != nil {
		t.Fatalf("good selector: %v", err)
	}
	bad := []byte(`
version: 1
sources:
  loki:
    kind: loki
    base_url: https://loki.example.internal
services:
  - id: app
    sources:
      loki:
        source: loki
        selector: '{app="x"} | json | line_format "{{.msg}}"'
`)
	if _, err := Parse(bad); err == nil {
		t.Fatal("expected bad selector rejection")
	}
	withControl := strings.ReplaceAll(string(good), `{app="x",host="y"} |= "error"`, "{app=\"x\u007fy\"}")
	if _, err := Parse([]byte(withControl)); err == nil {
		t.Fatal("control character must not be accepted")
	}
}

func TestParse_HealthchecksRequiresToken(t *testing.T) {
	raw := []byte(`
version: 1
sources:
  hc:
    kind: healthchecks
    base_url: https://hc.example.internal
services: []
`)
	_, err := Parse(raw)
	if err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("got %v", err)
	}
}

func TestParse_RejectsBlankTokenFile(t *testing.T) {
	raw := []byte(`
version: 1
sources:
  hc:
    kind: healthchecks
    base_url: https://hc.example.internal
    token_file: "   "
services: []
`)
	_, err := Parse(raw)
	if err == nil || !strings.Contains(err.Error(), "token_file") {
		t.Fatalf("got %v", err)
	}
}

func TestParse_RejectsBlankHealthchecksBinding(t *testing.T) {
	raw := []byte(`
version: 1
sources:
  hc:
    kind: healthchecks
    base_url: https://hc.example.internal
    token_env: HC_TOKEN
services:
  - id: backup
    sources:
      healthchecks:
        source: hc
        check_name: "   "
`)
	_, err := Parse(raw)
	if err == nil || !strings.Contains(err.Error(), "check_name, check_uuid, or check_tags required") {
		t.Fatalf("got %v", err)
	}
}

func TestParse_RejectsOversizedPublicFields(t *testing.T) {
	raw := []byte(`
version: 1
sources:
  gatus:
    kind: gatus
    base_url: https://gatus.example.internal
services:
  - id: app
    display_name: "` + strings.Repeat("x", 257) + `"
    sources:
      gatus:
        source: gatus
        endpoint_key: "` + strings.Repeat("y", 257) + `"
`)
	_, err := Parse(raw)
	if err == nil || !strings.Contains(err.Error(), "display_name") || !strings.Contains(err.Error(), "endpoint_key") {
		t.Fatalf("got %v", err)
	}
}

func TestParse_RejectsOversizedSourceReferenceAndRedaction(t *testing.T) {
	raw := []byte(`
version: 1
sources:
  gatus:
    kind: gatus
    base_url: https://gatus.example.internal
redact:
  - regex: "` + strings.Repeat("x", 4097) + `"
services:
  - id: app
    sources:
      gatus:
        source: ` + strings.Repeat("g", 257) + `
        endpoint_key: app
`)
	_, err := Parse(raw)
	if err == nil || !strings.Contains(err.Error(), "invalid source reference") ||
		!strings.Contains(err.Error(), "4096") {
		t.Fatalf("got %v", err)
	}
}

func TestParse_RejectsExcessiveCardinality(t *testing.T) {
	var raw strings.Builder
	raw.WriteString("version: 1\nsources:\n")
	for i := 0; i < maxSources+1; i++ {
		fmt.Fprintf(&raw, "  source%d:\n    kind: gatus\n    base_url: https://source%d.example.internal\n", i, i)
		if i == 0 {
			raw.WriteString("    headers:\n")
			for j := 0; j < maxHeadersPerSource+1; j++ {
				fmt.Fprintf(&raw, "      X-Test-%d: value\n", j)
			}
		}
	}
	raw.WriteString("services:\n")
	for i := 0; i < maxServices+1; i++ {
		fmt.Fprintf(&raw, "  - id: service-%d\n", i)
	}
	raw.WriteString("redact:\n")
	for i := 0; i < maxRedactRules+1; i++ {
		fmt.Fprintf(&raw, "  - exact: value-%d\n", i)
	}
	_, err := Parse([]byte(raw.String()))
	if err == nil ||
		!strings.Contains(err.Error(), "at most 64 sources") ||
		!strings.Contains(err.Error(), "at most 64 headers") ||
		!strings.Contains(err.Error(), "at most 1000 services") ||
		!strings.Contains(err.Error(), "at most 128 rules") {
		t.Fatalf("got %v", err)
	}
}

func TestParse_DuplicateServiceID(t *testing.T) {
	raw := []byte(`
version: 1
sources:
  gatus:
    kind: gatus
    base_url: https://gatus.example.internal
services:
  - id: a
  - id: a
`)
	_, err := Parse(raw)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("got %v", err)
	}
}

func TestParse_WindowBounds(t *testing.T) {
	raw := []byte(`
version: 1
limits:
  default_window: 48h
  max_incident_window: 24h
sources:
  gatus:
    kind: gatus
    base_url: https://gatus.example.internal
services: []
`)
	_, err := Parse(raw)
	if err == nil {
		t.Fatal("expected default > max error")
	}
}

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	raw := []byte(`
version: 1
sources:
  gatus:
    kind: gatus
    base_url: http://127.0.0.1:9
services: []
`)
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != 1 {
		t.Fatal(cfg.Version)
	}
}

func TestExampleConfigParses(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(raw); err != nil {
		t.Fatalf("config.example.yaml: %v", err)
	}
}

func TestConfigSchemaIsValidJSON(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("config.schema.json: %v", err)
	}
}

func TestLoadFile_RejectsGroupOrWorldAccessible(t *testing.T) {
	dir := t.TempDir()
	raw := []byte(`
version: 1
sources:
  gatus:
    kind: gatus
    base_url: http://127.0.0.1:9
services: []
`)
	// Mode 0644 must be rejected.
	loose := filepath.Join(dir, "loose.yaml")
	if err := os.WriteFile(loose, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loose, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFile(loose)
	if err == nil || !strings.Contains(err.Error(), "group or world accessible") {
		t.Fatalf("expected permission error, got %v", err)
	}

	// Mode 0600 must be accepted.
	tight := filepath.Join(dir, "tight.yaml")
	if err := os.WriteFile(tight, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(tight, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFile(tight)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != 1 {
		t.Fatal(cfg.Version)
	}
}

func TestResolveToken_Env(t *testing.T) {
	t.Setenv("HC_TEST_TOKEN", "tok-value-xyz")
	tok, err := ResolveToken(Source{TokenEnv: "HC_TEST_TOKEN"})
	if err != nil || tok != "tok-value-xyz" {
		t.Fatalf("%q %v", tok, err)
	}
}

func TestResolveToken_RejectsBlankEnv(t *testing.T) {
	t.Setenv("HC_TEST_TOKEN", "   ")
	if _, err := ResolveToken(Source{TokenEnv: "HC_TEST_TOKEN"}); err == nil {
		t.Fatal("expected blank environment variable rejection")
	}
}

func TestResolveToken_FilePerms(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tok")
	if err := os.WriteFile(p, []byte("secret-token\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ResolveToken(Source{TokenFile: p})
	if err == nil || !strings.Contains(err.Error(), "group or world") {
		t.Fatalf("got %v", err)
	}
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	tok, err := ResolveToken(Source{TokenFile: p})
	if err != nil || tok != "secret-token" {
		t.Fatalf("%q %v", tok, err)
	}
}

func TestResolveToken_RejectsEmptyFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tok")
	if err := os.WriteFile(p, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveToken(Source{TokenFile: p}); err == nil {
		t.Fatal("expected empty token file rejection")
	}
}

func TestResolveToken_RejectsFileURLHost(t *testing.T) {
	if _, err := ResolveToken(Source{TokenFile: "file://remote.example/secret"}); err == nil {
		t.Fatal("expected token file URL host rejection")
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte("version: 1\nsources: {}\nservices: []\n"))
	f.Add([]byte("version: 1\nsources:\n  a:\n    kind: gatus\n    base_url: https://x\n"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = Parse(raw)
	})
}
