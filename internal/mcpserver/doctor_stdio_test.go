package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
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

// This test detects doctor exit codes, missing per-source diagnostics, and a
// validate command that unexpectedly contacts sources.
func TestStdioDoctor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var calls atomic.Int32
	handler := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if r.Method != http.MethodGet {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			_, _ = w.Write([]byte(body))
		}
	}
	goodBody := `[{"name":"good","key":"good","results":[{"success":true,"status":200,"timestamp":"` +
		time.Now().UTC().Format(time.RFC3339) + `"}]}]`
	good := httptest.NewServer(handler(goodBody))
	defer good.Close()
	malformed := httptest.NewServer(handler(`{not-json`))
	defer malformed.Close()
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		time.Sleep(time.Second)
		_, _ = w.Write([]byte(goodBody))
	}))
	defer slow.Close()
	docker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer docker.Close()
	loki := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer loki.Close()
	tlsServer := httptest.NewUnstartedServer(handler(goodBody))
	tlsServer.Config.ErrorLog = log.New(io.Discard, "", 0)
	tlsServer.StartTLS()
	defer tlsServer.Close()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configText := `version: 1
limits:
  per_source_timeout: 300ms
  total_timeout: 10s
sources:
  gatus-good:
    kind: gatus
    base_url: ` + good.URL + `
  gatus-malformed:
    kind: gatus
    base_url: ` + malformed.URL + `
  gatus-slow:
    kind: gatus
    base_url: ` + slow.URL + `
  gatus-tls:
    kind: gatus
    base_url: ` + tlsServer.URL + `
  docker-unauth:
    kind: docker
    base_url: ` + docker.URL + `
  loki-forbidden:
    kind: loki
    base_url: ` + loki.URL + `
services:
  - id: good
    sources:
      gatus: { source: gatus-good, endpoint_key: good }
  - id: unmatched
    sources:
      gatus: { source: gatus-good, endpoint_key: missing }
  - id: malformed
    sources:
      gatus: { source: gatus-malformed, endpoint_key: good }
  - id: slow
    sources:
      gatus: { source: gatus-slow, endpoint_key: good }
  - id: tls
    sources:
      gatus: { source: gatus-tls, endpoint_key: good }
  - id: docker
    sources:
      docker: { source: docker-unauth, container_name: app }
  - id: loki
    sources:
      loki: { source: loki-forbidden, selector: '{container="app"}' }
  - id: mixed
    sources:
      gatus: { source: gatus-good, endpoint_key: good }
      loki: { source: loki-forbidden, selector: '{container="app"}' }
`
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := stdioTestBinary(t, ctx)

	var stdout, stderr strings.Builder
	command := exec.CommandContext(ctx, binary, "--config", configPath, "--doctor", "--json")
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	var exitErr *exec.ExitError
	if err == nil || !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("doctor exit=%v, want 1\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	var report struct {
		Version  string `json:"version"`
		ReadOnly bool   `json:"readonly"`
		Complete bool   `json:"complete"`
		Sources  []struct {
			Name         string   `json:"name"`
			Kind         string   `json:"kind"`
			Status       string   `json:"status"`
			Reason       string   `json:"reason"`
			Complete     bool     `json:"complete"`
			Capabilities []string `json:"capabilities"`
			Bindings     []struct {
				ServiceID string `json:"service_id"`
				Status    string `json:"status"`
				Reason    string `json:"reason"`
			} `json:"bindings"`
		} `json:"sources"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &report); err != nil {
		t.Fatalf("doctor JSON: %v\n%s", err, stdout.String())
	}
	if report.Version == "" || !report.ReadOnly || report.Complete {
		t.Fatalf("unexpected report: %+v", report)
	}
	want := map[string]struct {
		status string
		reason string
	}{
		"gatus-good":      {"partial", "unmatched_binding"},
		"gatus-malformed": {"error", "upstream_error"},
		"gatus-slow":      {"timeout", "timeout"},
		"gatus-tls":       {"error", "upstream_error"},
		"docker-unauth":   {"error", "authentication_failed"},
		"loki-forbidden":  {"error", "authentication_failed"},
	}
	if len(report.Sources) != len(want) {
		t.Fatalf("sources=%d, want=%d", len(report.Sources), len(want))
	}
	for _, source := range report.Sources {
		expected, ok := want[source.Name]
		if !ok {
			t.Fatalf("unexpected source %s", source.Name)
		}
		if source.Status != expected.status || source.Reason != expected.reason || source.Complete {
			t.Fatalf("%s status=%s reason=%s complete=%v", source.Name, source.Status, source.Reason, source.Complete)
		}
		if len(source.Capabilities) == 0 || len(source.Bindings) == 0 {
			t.Fatalf("%s did not report capabilities or bindings: %+v", source.Name, source)
		}
	}
	if strings.Contains(stdout.String(), good.URL) || strings.Contains(stderr.String(), good.URL) {
		t.Fatal("doctor output disclosed a source URL")
	}

	beforeValidate := calls.Load()
	var validateOut, validateErr strings.Builder
	validate := exec.CommandContext(ctx, binary, "--config", configPath, "--validate")
	validate.Stdout = &validateOut
	validate.Stderr = &validateErr
	if err := validate.Run(); err != nil {
		t.Fatalf("validate failed: %v\n%s", err, validateErr.String())
	}
	if calls.Load() != beforeValidate {
		t.Fatalf("--validate sent %d requests", calls.Load()-beforeValidate)
	}

	successPath := filepath.Join(dir, "success.yaml")
	successText := `version: 1
sources:
  gatus-good:
    kind: gatus
    base_url: ` + good.URL + `
services:
  - id: good
    sources:
      gatus: { source: gatus-good, endpoint_key: good }
`
	if err := os.WriteFile(successPath, []byte(successText), 0o600); err != nil {
		t.Fatal(err)
	}
	var successOut, successErr strings.Builder
	success := exec.CommandContext(ctx, binary, "--config", successPath, "--doctor")
	success.Stdout = &successOut
	success.Stderr = &successErr
	if err := success.Run(); err != nil {
		t.Fatalf("successful doctor failed: %v\n%s\n%s", err, successOut.String(), successErr.String())
	}
	if !strings.Contains(successOut.String(), "complete=true") ||
		!strings.Contains(successOut.String(), "doctor:") {
		t.Fatalf("unexpected human output: %s", successOut.String())
	}
}
