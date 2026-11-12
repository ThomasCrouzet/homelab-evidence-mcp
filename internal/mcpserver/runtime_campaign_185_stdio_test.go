package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/evidence"
)

type runtimeDoctorReport struct {
	Version  string `json:"version"`
	Complete bool   `json:"complete"`
	Sources  []struct {
		Name     string `json:"name"`
		Kind     string `json:"kind"`
		Status   string `json:"status"`
		Reason   string `json:"reason"`
		Error    string `json:"error"`
		Complete bool   `json:"complete"`
		Bindings []struct {
			ServiceID string `json:"service_id"`
			Status    string `json:"status"`
			Reason    string `json:"reason"`
			Complete  bool   `json:"complete"`
		} `json:"bindings"`
	} `json:"sources"`
}

// runtimeDoctor runs the executable doctor and gives its report and exit code.
func runtimeDoctor(t *testing.T, ctx context.Context, binary, configPath string) (runtimeDoctorReport, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	command := exec.CommandContext(ctx, binary, "--config", configPath, "--doctor", "--json")
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("run doctor: %v\n%s", err, stderr.String())
		}
		code = exitErr.ExitCode()
	}
	var report runtimeDoctorReport
	if decodeErr := json.Unmarshal(stdout.Bytes(), &report); decodeErr != nil {
		t.Fatalf("decode doctor report: %v\nstdout=%s\nstderr=%s", decodeErr, stdout.String(), stderr.String())
	}
	return report, code
}

// This test detects a healthy ntfy binding that fails the doctor only because
// the replay buffer cannot prove an unbounded history. It also detects a
// diagnostic path that hides real ntfy failures, and evidence that claims
// historical completeness.
func TestStdioDoctorHealthyNtfyBinding(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	now := time.Now().UTC().Truncate(time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/healthy/"):
			_, _ = fmt.Fprintf(w,
				"{\"id\":\"notice-1\",\"time\":%d,\"event\":\"message\",\"topic\":\"alerts\",\"message\":\"disk full\"}\n",
				now.Add(-2*time.Minute).Unix(),
			)
		case strings.HasPrefix(r.URL.Path, "/empty/"):
			w.WriteHeader(http.StatusOK)
		case strings.HasPrefix(r.URL.Path, "/wrong-topic/"):
			_, _ = fmt.Fprintf(w,
				"{\"id\":\"notice-2\",\"time\":%d,\"event\":\"message\",\"topic\":\"other\",\"message\":\"wrong topic\"}\n",
				now.Add(-2*time.Minute).Unix(),
			)
		case strings.HasPrefix(r.URL.Path, "/malformed/"):
			_, _ = w.Write([]byte("{not-json"))
		case strings.HasPrefix(r.URL.Path, "/unauthorized/"):
			w.WriteHeader(http.StatusUnauthorized)
		case strings.HasPrefix(r.URL.Path, "/stalled/"):
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	binary := stdioTestBinary(t, ctx)

	healthy := fmt.Sprintf(`version: 1
limits:
  per_source_timeout: 2s
  total_timeout: 10s
  source_cache_ttl: 0s
sources:
  ntfy-healthy:
    kind: ntfy
    base_url: %s/healthy
  ntfy-empty:
    kind: ntfy
    base_url: %s/empty
services:
  - id: healthy-alerts
    sources:
      ntfy: { source: ntfy-healthy, topic: alerts }
  - id: empty-alerts
    sources:
      ntfy: { source: ntfy-empty, topic: alerts }
`, server.URL, server.URL)
	healthyPath := runtimeWriteConfig(t, healthy)

	report, code := runtimeDoctor(t, ctx, binary, healthyPath)
	if code != 0 {
		t.Fatalf("doctor exit=%d, want 0: %+v", code, report)
	}
	if !report.Complete {
		t.Fatalf("doctor report is incomplete: %+v", report)
	}
	if len(report.Sources) != 2 {
		t.Fatalf("doctor sources=%d, want 2", len(report.Sources))
	}
	for _, source := range report.Sources {
		// A healthy source may report empty_result for an empty replay, but it
		// must not report a retention or validity failure.
		if source.Status != "ok" || !source.Complete || source.Reason == "retention_limited" ||
			source.Reason == "partial_invalid_records" {
			t.Fatalf("healthy ntfy source=%+v", source)
		}
		if len(source.Bindings) != 1 || source.Bindings[0].Status != "ok" || !source.Bindings[0].Complete {
			t.Fatalf("healthy ntfy binding=%+v", source.Bindings)
		}
	}
	positivePath := healthyPath
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("retention_limited")) {
		t.Fatalf("doctor claimed historical retention state: %s", encoded)
	}

	// The evidence path must keep the bounded-replay limitation.
	session := runtimeConnect(t, ctx, binary, positivePath)
	raw := executableE2ECall(t, ctx, session, "incident_context", map[string]any{
		"service_id": "healthy-alerts",
		"duration":   "1h",
	})
	outcome := runtimeFindSource(t, runtimeSources(t, raw), "ntfy-healthy", evidence.SourceNtfy)
	if outcome.Status != "ok" || outcome.Complete || !outcome.RetentionLimited || outcome.Reason != "retention_limited" {
		t.Fatalf("incident evidence lost the replay limit: %+v", outcome)
	}

	broken := fmt.Sprintf(`version: 1
limits:
  per_source_timeout: 300ms
  total_timeout: 10s
  source_cache_ttl: 0s
sources:
  ntfy-wrong-topic:
    kind: ntfy
    base_url: %s/wrong-topic
  ntfy-malformed:
    kind: ntfy
    base_url: %s/malformed
  ntfy-unauthorized:
    kind: ntfy
    base_url: %s/unauthorized
  ntfy-stalled:
    kind: ntfy
    base_url: %s/stalled
services:
  - id: wrong-topic
    sources:
      ntfy: { source: ntfy-wrong-topic, topic: alerts }
  - id: malformed
    sources:
      ntfy: { source: ntfy-malformed, topic: alerts }
  - id: unauthorized
    sources:
      ntfy: { source: ntfy-unauthorized, topic: alerts }
  - id: stalled
    sources:
      ntfy: { source: ntfy-stalled, topic: alerts }
`, server.URL, server.URL, server.URL, server.URL)
	brokenReport, brokenCode := runtimeDoctor(t, ctx, binary, runtimeWriteConfig(t, broken))
	if brokenCode != 1 {
		t.Fatalf("doctor exit=%d for broken ntfy sources, want 1: %+v", brokenCode, brokenReport)
	}
	if brokenReport.Complete {
		t.Fatalf("broken doctor report is complete: %+v", brokenReport)
	}
	want := map[string]struct {
		status string
		reason string
	}{
		"ntfy-wrong-topic":  {status: "partial", reason: "partial_invalid_records"},
		"ntfy-malformed":    {status: "error", reason: "upstream_error"},
		"ntfy-unauthorized": {status: "error", reason: "authentication_failed"},
		"ntfy-stalled":      {status: "timeout", reason: "timeout"},
	}
	if len(brokenReport.Sources) != len(want) {
		t.Fatalf("broken sources=%d, want=%d", len(brokenReport.Sources), len(want))
	}
	for _, source := range brokenReport.Sources {
		expected, ok := want[source.Name]
		if !ok {
			t.Fatalf("unexpected source %s", source.Name)
		}
		if source.Status != expected.status || source.Reason != expected.reason || source.Complete {
			t.Fatalf("%s status=%s reason=%s complete=%v, want %+v",
				source.Name, source.Status, source.Reason, source.Complete, expected)
		}
	}
}
