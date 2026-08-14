package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun_Version(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"--version"}, &out, &errb)
	if code != 0 {
		t.Fatalf("code=%d err=%s", code, errb.String())
	}
	if strings.TrimSpace(out.String()) == "" {
		t.Fatal("empty version")
	}
}

func TestRun_MissingConfig(t *testing.T) {
	var out, errb bytes.Buffer
	code := run(nil, &out, &errb)
	if code != 2 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(errb.String(), "--config") {
		t.Fatal(errb.String())
	}
}

func TestRun_RejectsInvalidLogLevel(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"--config", "unused.yaml", "--log-level", "verbose"}, &out, &errb)
	if code != 2 || !strings.Contains(errb.String(), "--log-level") {
		t.Fatalf("code=%d err=%s", code, errb.String())
	}
}

func TestRun_RejectsPositionalArguments(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"unexpected"}, &out, &errb)
	if code != 2 || !strings.Contains(errb.String(), "positional") {
		t.Fatalf("code=%d err=%s", code, errb.String())
	}
}

func TestRun_ValidateOK(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.yaml")
	raw := `
version: 1
sources:
  gatus:
    kind: gatus
    base_url: https://gatus.example.internal
services:
  - id: media
    sources:
      gatus:
        source: gatus
        endpoint_key: media_app
`
	if err := os.WriteFile(p, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code := run([]string{"--config", p, "--validate"}, &out, &errb)
	if code != 0 {
		t.Fatalf("code=%d err=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "config ok") {
		t.Fatal(out.String())
	}
}

func TestRun_ValidateDoesNotOpenAuditFile(t *testing.T) {
	dir := t.TempDir()
	auditPath := filepath.Join(dir, "audit.jsonl")
	p := filepath.Join(dir, "cfg.yaml")
	raw := fmt.Sprintf(`
version: 1
audit:
  file: %s
sources:
  gatus:
    kind: gatus
    base_url: https://gatus.example.internal
`, auditPath)
	if err := os.WriteFile(p, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code := run([]string{"--config", p, "--validate"}, &out, &errb)
	if code != 0 {
		t.Fatalf("code=%d err=%s", code, errb.String())
	}
	if _, err := os.Stat(auditPath); !os.IsNotExist(err) {
		t.Fatalf("validate opened audit file: %v", err)
	}
}

func TestRun_ValidateBad(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(p, []byte("version: 99\nsources: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code := run([]string{"--config", p, "--validate"}, &out, &errb)
	if code == 0 {
		t.Fatal("expected failure")
	}
}
