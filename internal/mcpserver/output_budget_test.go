package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/audit"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/config"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/evidence"
)

func budgetApp(t *testing.T, lokiURL string) *App {
	t.Helper()
	raw := fmt.Sprintf(`version: 1
limits:
  default_window: 15m
  max_log_lines: 200
  max_evidence_items: 200
  max_incident_response_bytes: 8192
  source_cache_ttl: 0s
sources:
  loki:
    kind: loki
    base_url: %s
services:
  - id: logs
    sources:
      loki: { source: loki, selector: '{container="app"}' }
`, lokiURL)
	cfg, err := config.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	app, err := NewApp(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), audit.New(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func lokiBulkServer(t *testing.T) *httptest.Server {
	t.Helper()
	base := time.Now().UTC().Add(-time.Minute)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		values := make([][]string, 0, 120)
		for index := 0; index < 120; index++ {
			values = append(values, []string{
				strconv.FormatInt(base.Add(time.Duration(index)*time.Second).UnixNano(), 10),
				"line-" + strconv.Itoa(index) + " " + strings.Repeat("x", 500),
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"resultType": "streams",
				"result": []map[string]any{{
					"stream": map[string]string{"container": "app"},
					"values": values,
				}},
			},
		})
	}))
	t.Cleanup(server.Close)
	return server
}

func TestSearchLogs_ResponseByteBudget(t *testing.T) {
	app := budgetApp(t, lokiBulkServer(t).URL)
	res, out, err := app.toolSearchLogs(context.Background(), nil, searchLogsIn{ServiceID: "logs", Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("budget should trim, not fail: %+v", res)
	}
	body, ok := out.(SearchLogsOut)
	if !ok {
		t.Fatalf("output type %T", out)
	}
	if body.ByteSize > body.ByteBudget || !body.ByteBudgetTruncated || body.OmittedItems == 0 {
		t.Fatalf("budget=%d size=%d truncated=%v omitted=%d", body.ByteBudget, body.ByteSize, body.ByteBudgetTruncated, body.OmittedItems)
	}
	if body.DeliveryComplete {
		t.Fatal("trimmed response claims complete delivery")
	}
}

func TestIncident_NonCompactResponseByteBudget(t *testing.T) {
	app := budgetApp(t, lokiBulkServer(t).URL)
	res, out, err := app.toolIncidentContext(context.Background(), nil, incidentIn{ServiceID: "logs"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("budget should trim, not fail: %+v", res)
	}
	body, ok := out.(IncidentContextOut)
	if !ok {
		t.Fatalf("output type %T", out)
	}
	bundle := evidence.Bundle(body)
	if bundle.ByteSize > bundle.ByteBudget || !bundle.ByteBudgetTruncated || bundle.DeliveryComplete {
		t.Fatalf("budget=%d size=%d truncated=%v delivery=%v", bundle.ByteBudget, bundle.ByteSize, bundle.ByteBudgetTruncated, bundle.DeliveryComplete)
	}
}

func TestGetEvidence_RejectsItemAboveBudget(t *testing.T) {
	app := budgetApp(t, lokiBulkServer(t).URL)
	app.Cache.Put(evidence.Item{
		ID: "big", Source: evidence.SourceLoki, SourceID: "big",
		ObservedAt: time.Now().UTC(), RetrievedAt: time.Now().UTC(),
		Summary: "big", Attributes: map[string]any{"line": strings.Repeat("x", 20000)},
	})
	res, _, err := app.toolGetEvidence(context.Background(), nil, getEvidenceIn{ID: "big"})
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("oversized item returned success: %+v", res)
	}
}

func healthchecksBulkApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("HC_BUDGET_TOKEN", "fixture-token")
	checks := make([]map[string]any, 0, 100)
	for index := 0; index < 100; index++ {
		checks = append(checks, map[string]any{
			"name": fmt.Sprintf("check-%03d", index), "slug": fmt.Sprintf("check-%03d", index),
			"status": "down", "grace": 60, "tags": "bulk",
			"unique_key": fmt.Sprintf("%032d", index),
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"checks": checks})
	}))
	t.Cleanup(server.Close)
	raw := fmt.Sprintf(`version: 1
limits:
  max_incident_response_bytes: 8192
  source_cache_ttl: 0s
sources:
  hc:
    kind: healthchecks
    base_url: %s
    token_env: HC_BUDGET_TOKEN
services:
  - id: crons
    sources:
      healthchecks: { source: hc, check_tags: [bulk] }
`, server.URL)
	cfg, err := config.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	app, err := NewApp(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), audit.New(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func TestServiceStatusAndFailedCrons_ResponseByteBudget(t *testing.T) {
	app := healthchecksBulkApp(t)
	// A global failed_crons call reads every check and trims the payload.
	res, out, err := app.toolFailedCrons(context.Background(), nil, failedCronsIn{Duration: "1h"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("failed_crons budget should trim: %+v", res)
	}
	crons := out.(FailedCronsOut)
	if crons.ByteSize > crons.ByteBudget || !crons.ByteBudgetTruncated || crons.OmittedItems == 0 {
		t.Fatalf("crons budget=%d size=%d truncated=%v omitted=%d", crons.ByteBudget, crons.ByteSize, crons.ByteBudgetTruncated, crons.OmittedItems)
	}

	res, out, err = app.toolServiceStatus(context.Background(), nil, serviceIDIn{ServiceID: "crons"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("service_status budget should trim: %+v", res)
	}
	status := out.(ServiceStatusOut)
	if status.ByteSize > status.ByteBudget || !status.ByteBudgetTruncated || status.OmittedItems == 0 {
		t.Fatalf("status budget=%d size=%d truncated=%v omitted=%d", status.ByteBudget, status.ByteSize, status.ByteBudgetTruncated, status.OmittedItems)
	}
}
