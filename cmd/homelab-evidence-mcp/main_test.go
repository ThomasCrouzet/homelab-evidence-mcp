package main

import (
	"bytes"
	"encoding/json"
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

func TestRun_JsonRequiresDoctor(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"--config", "unused.yaml", "--json"}, &out, &errb)
	if code != 2 || !strings.Contains(errb.String(), "--json") {
		t.Fatalf("code=%d err=%s", code, errb.String())
	}
}

func TestRun_RejectsDoctorWithValidate(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"--config", "unused.yaml", "--doctor", "--validate"}, &out, &errb)
	if code != 2 || !strings.Contains(errb.String(), "--doctor") {
		t.Fatalf("code=%d err=%s", code, errb.String())
	}
}

func TestRun_ValidateBadExitCode(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(p, []byte("version: 99\nsources: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"--config", p, "--validate"}, &out, &errb); code != 2 {
		t.Fatalf("code=%d err=%s", code, errb.String())
	}
}

func TestRun_DoctorConfigErrorIsExitTwoAndSanitized(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private-operator-path")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(p, []byte("version: 99\nsources: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"--config", p, "--doctor"}, &out, &errb); code != 2 {
		t.Fatalf("code=%d err=%s", code, errb.String())
	}
	if strings.Contains(errb.String(), dir) || strings.Contains(out.String(), dir) {
		t.Fatalf("private path leaked: out=%q err=%q", out.String(), errb.String())
	}

	out.Reset()
	errb.Reset()
	if code := run([]string{"--config", p, "--doctor", "--json"}, &out, &errb); code != 2 {
		t.Fatalf("json code=%d err=%s", code, errb.String())
	}
	var body map[string]string
	if err := json.Unmarshal(out.Bytes(), &body); err != nil {
		t.Fatalf("stdout is not JSON: %v %q", err, out.String())
	}
	if body["reason"] != "configuration_error" || body["error"] == "" {
		t.Fatalf("body=%v", body)
	}
	if strings.Contains(out.String(), dir) {
		t.Fatalf("private path leaked in JSON: %s", out.String())
	}
}

func TestRun_StartupPathIsSanitized(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private-operator-path")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(dir, "missing-token")
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	raw := fmt.Sprintf(`version: 1
sources:
  hc:
    kind: healthchecks
    base_url: https://healthchecks.example.internal
    token_file: %s
services:
  - id: cron
    sources:
      healthchecks: { source: hc, check_name: backup }
`, tokenPath)
	if err := os.WriteFile(p, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"--config", p}, &out, &errb); code != 1 {
		t.Fatalf("code=%d err=%s", code, errb.String())
	}
	if strings.Contains(errb.String(), dir) || strings.Contains(errb.String(), "missing-token") {
		t.Fatalf("private token path leaked: %s", errb.String())
	}
}
