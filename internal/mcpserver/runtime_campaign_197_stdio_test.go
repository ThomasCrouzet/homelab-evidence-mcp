package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/evidence"
)

// This test detects typed adapter failures that lose their machine-readable
// reason, an extra upstream request after a budget rejection, and a regression
// in the existing ambiguity mapping.
func TestStdioTypedAdapterReasonCodes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	t.Setenv("HC_RUNTIME_TOKEN", "runtime-token")

	now := time.Now().UTC()
	gatus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/endpoints/statuses" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`[
			{"name":"first","key":"dup","results":[{"success":true,"status":200,"timestamp":"` +
			now.Add(-time.Minute).Format(time.RFC3339) + `"}]},
			{"name":"second","key":"dup","results":[{"success":true,"status":200,"timestamp":"` +
			now.Add(-time.Minute).Format(time.RFC3339) + `"}]}
		]`))
	}))
	defer gatus.Close()
	docker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/containers/json" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"Id": "one", "Names": []string{"/dup-app"}, "State": "running", "Image": "app:1"},
			{"Id": "two", "Names": []string{"/dup-app"}, "State": "running", "Image": "app:1"},
		})
	}))
	defer docker.Close()
	beszel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/collections/systems/records" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{
			{"id": "one", "name": "dup-host", "status": "up"},
			{"id": "two", "name": "dup-host", "status": "up"},
		}})
	}))
	defer beszel.Close()
	var healthchecksCalls atomic.Int32
	healthchecks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/checks/" {
			http.NotFound(w, r)
			return
		}
		healthchecksCalls.Add(1)
		_, _ = w.Write([]byte(`{"checks":[{"name":"runtime-cron","slug":"runtime-cron","tags":"shared","unique_key":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","status":"down","grace":60,"last_ping":"` +
			now.Add(-time.Minute).Format(time.RFC3339) + `"}]}`))
	}))
	defer healthchecks.Close()

	raw := fmt.Sprintf(`version: 1
limits:
  per_source_timeout: 2s
  total_timeout: 10s
  max_source_requests_per_minute: 1
  source_cache_ttl: 0s
sources:
  gatus-dup:
    kind: gatus
    base_url: %s
  docker-dup:
    kind: docker
    base_url: %s
  beszel-dup:
    kind: beszel
    base_url: %s
  hc-budget:
    kind: healthchecks
    base_url: %s
    token_env: HC_RUNTIME_TOKEN
services:
  - id: gatus-dup
    sources:
      gatus: { source: gatus-dup, endpoint_key: dup }
  - id: docker-dup
    sources:
      docker: { source: docker-dup, container_name: dup-app }
  - id: beszel-dup
    sources:
      beszel: { source: beszel-dup, system_name: dup-host }
  - id: hc-budget
    sources:
      healthchecks: { source: hc-budget, check_tags: [shared] }
`, gatus.URL, docker.URL, beszel.URL, healthchecks.URL)
	binary := stdioTestBinary(t, ctx)
	session := runtimeConnect(t, ctx, binary, runtimeWriteConfig(t, raw))

	for _, kind := range []evidence.SourceKind{evidence.SourceGatus, evidence.SourceDocker, evidence.SourceBeszel} {
		kind := kind
		t.Run("ambiguity_"+string(kind), func(t *testing.T) {
			rawResult := executableE2ECall(t, ctx, session, "service_status", map[string]any{
				"service_id": fmt.Sprintf("%s-dup", kind),
			})
			outcome := runtimeFindSource(t, runtimeSources(t, rawResult), fmt.Sprintf("%s-dup", kind), kind)
			if outcome.Status != "error" || outcome.Reason != "ambiguous_binding" || outcome.Complete {
				t.Fatalf("%s ambiguity outcome=%+v", kind, outcome)
			}
		})
	}

	t.Run("list_budget", func(t *testing.T) {
		// The first status call consumes the single request of the source budget.
		statusRaw := executableE2ECall(t, ctx, session, "service_status", map[string]any{"service_id": "hc-budget"})
		statusOutcome := runtimeFindSource(t, runtimeSources(t, statusRaw), "hc-budget", evidence.SourceHealthchecks)
		if statusOutcome.Status != "ok" {
			t.Fatalf("healthchecks status outcome=%+v", statusOutcome)
		}
		before := healthchecksCalls.Load()
		cronsRaw := executableE2ECall(t, ctx, session, "failed_crons", map[string]any{"duration": "1h"})
		cronsOutcome := runtimeFindSource(t, runtimeSources(t, cronsRaw), "hc-budget", evidence.SourceHealthchecks)
		if cronsOutcome.Status != "error" || cronsOutcome.Reason != "request_budget_exceeded" || cronsOutcome.Complete {
			t.Fatalf("budget outcome=%+v", cronsOutcome)
		}
		if after := healthchecksCalls.Load(); after != before {
			t.Fatalf("a budget rejection made %d extra upstream request(s)", after-before)
		}
	})
}

// This test detects a per-check history budget that loses partial evidence or
// the explicit request counters.
func TestStdioHealthchecksHistoryBudgetKeepsPartialEvidence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	t.Setenv("HC_RUNTIME_TOKEN", "runtime-token")
	now := time.Now().UTC()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/checks/":
			_, _ = w.Write([]byte(`{"checks":[
				{"name":"cron-a","slug":"cron-a","tags":"cron","unique_key":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","status":"up"},
				{"name":"cron-b","slug":"cron-b","tags":"cron","unique_key":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","status":"up"}
			]}`))
		case strings.HasSuffix(r.URL.Path, "/flips/"):
			_, _ = fmt.Fprintf(w, `{"flips":[{"timestamp":%q,"up":0}]}`,
				now.Add(-30*time.Minute).Format(time.RFC3339))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	raw := fmt.Sprintf(`version: 1
limits:
  per_source_timeout: 2s
  total_timeout: 10s
  max_source_requests_per_minute: 2
  source_cache_ttl: 0s
sources:
  hc-history:
    kind: healthchecks
    base_url: %s
    token_env: HC_RUNTIME_TOKEN
services:
  - id: history-svc
    sources:
      healthchecks: { source: hc-history, check_tags: [cron], history_mode: transitions }
`, server.URL)
	binary := stdioTestBinary(t, ctx)
	session := runtimeConnect(t, ctx, binary, runtimeWriteConfig(t, raw))

	incidentRaw := executableE2ECall(t, ctx, session, "incident_context", map[string]any{
		"service_id": "history-svc",
		"duration":   "1h",
	})
	outcome := runtimeFindSource(t, runtimeSources(t, incidentRaw), "hc-history", evidence.SourceHealthchecks)
	if outcome.Status != "ok" || outcome.Complete || outcome.Reason != "request_budget_exceeded" {
		t.Fatalf("history budget outcome=%+v", outcome)
	}
	if outcome.QueriedChecks != 2 || outcome.FailedChecks != 1 {
		t.Fatalf("history request counters=%+v", outcome)
	}
	var parsed struct {
		Items []evidence.Item `json:"items"`
	}
	executableE2EDecode(t, incidentRaw, &parsed)
	if len(parsed.Items) == 0 {
		t.Fatalf("history budget discarded every item: %s", incidentRaw)
	}
}
