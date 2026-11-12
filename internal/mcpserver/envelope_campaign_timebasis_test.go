package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/audit"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/config"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/evidence"
)

// TestFailedCronsTimeBasisAfterTrim detects a time_basis that describes the
// discarded items instead of the final retained items.
func TestFailedCronsTimeBasisAfterTrim(t *testing.T) {
	t.Run("no trim reports mixed", func(t *testing.T) {
		app := failedCronsCampaignApp(t, 1<<20, 200)
		res, out, err := app.toolFailedCrons(context.Background(), nil, failedCronsIn{Duration: "24h"})
		if err != nil {
			t.Fatal(err)
		}
		if res.IsError {
			t.Fatalf("unexpected tool error: %s", errorBodyText(t, res))
		}
		crons := out.(FailedCronsOut)
		if crons.TimeBasis != "mixed" {
			t.Fatalf("time_basis=%s, want mixed", crons.TimeBasis)
		}
		if got := envelopeWireSize(t, crons); got != crons.ByteSize {
			t.Fatalf("byte_size=%d, final envelope=%d", crons.ByteSize, got)
		}
	})

	t.Run("count limit keeps current snapshots", func(t *testing.T) {
		app := failedCronsCampaignApp(t, 1<<20, 3)
		_, out, err := app.toolFailedCrons(context.Background(), nil, failedCronsIn{Duration: "24h"})
		if err != nil {
			t.Fatal(err)
		}
		crons := out.(FailedCronsOut)
		if !crons.Truncated {
			t.Fatal("count limit did not report truncation")
		}
		if crons.TimeBasis != "current_snapshot" {
			t.Fatalf("time_basis=%s after count trim, want current_snapshot", crons.TimeBasis)
		}
		current, historical := failedCronCategories(crons.Items)
		if !current || historical {
			t.Fatalf("retained categories current=%v historical=%v", current, historical)
		}
	})

	t.Run("byte limit keeps historical transitions", func(t *testing.T) {
		app := failedCronsCampaignApp(t, 8192, 200)
		start := time.Now().UTC().Add(-23*time.Hour - 50*time.Minute).Format(time.RFC3339)
		end := time.Now().UTC().Add(10 * time.Minute).Format(time.RFC3339)
		res, out, err := app.toolFailedCrons(context.Background(), nil, failedCronsIn{Start: start, End: end})
		if err != nil {
			t.Fatal(err)
		}
		if res.IsError {
			t.Fatalf("byte trimming must not fail: %s", errorBodyText(t, res))
		}
		crons := out.(FailedCronsOut)
		if !crons.ByteBudgetTruncated || crons.OmittedItems == 0 {
			t.Fatalf("byte trimming was not applied: truncated=%v omitted=%d",
				crons.ByteBudgetTruncated, crons.OmittedItems)
		}
		current, historical := failedCronCategories(crons.Items)
		if current || !historical {
			t.Fatalf("retained categories current=%v historical=%v", current, historical)
		}
		if crons.TimeBasis != "historical_transitions" {
			t.Fatalf("time_basis=%s after byte trim, want historical_transitions", crons.TimeBasis)
		}
		if got := envelopeWireSize(t, crons); got != crons.ByteSize {
			t.Fatalf("byte_size=%d, final envelope=%d", crons.ByteSize, got)
		}
	})

	t.Run("empty final result reports unknown", func(t *testing.T) {
		app := failedCronsCampaignApp(t, 1<<20, 200)
		_, out, err := app.toolFailedCrons(context.Background(), nil, failedCronsIn{Duration: "24h"})
		if err != nil {
			t.Fatal(err)
		}
		fitted := out.(FailedCronsOut)
		budget := emptyEnvelopeBudget(t, fitted)

		app.Cfg.Limits.MaxIncidentBytes = budget
		res, out, err := app.toolFailedCrons(context.Background(), nil, failedCronsIn{Duration: "24h"})
		if err != nil {
			t.Fatal(err)
		}
		if res.IsError {
			t.Fatalf("metadata budget %d must keep the response: %s", budget, errorBodyText(t, res))
		}
		crons := out.(FailedCronsOut)
		if len(crons.Items) != 0 {
			t.Fatalf("budget %d retained %d item(s), want none", budget, len(crons.Items))
		}
		if crons.TimeBasis != "unknown" {
			t.Fatalf("time_basis=%s with no retained item, want unknown", crons.TimeBasis)
		}
		if crons.OmittedItems != len(fitted.Items) {
			t.Fatalf("omitted_items=%d, want %d", crons.OmittedItems, len(fitted.Items))
		}
		if got := envelopeWireSize(t, crons); got != crons.ByteSize {
			t.Fatalf("byte_size=%d, final envelope=%d", crons.ByteSize, got)
		}
		if crons.ByteSize > budget {
			t.Fatalf("byte_size=%d exceeds budget=%d", crons.ByteSize, budget)
		}
	})
}

func failedCronCategories(items []evidence.Item) (current, historical bool) {
	for _, item := range items {
		if item.Source != evidence.SourceHealthchecks {
			continue
		}
		if value, ok := item.Attributes["current"].(bool); ok {
			if value {
				current = true
			} else {
				historical = true
			}
		}
	}
	return current, historical
}

// emptyEnvelopeBudget derives a budget that fits the response metadata but no
// evidence item.
func emptyEnvelopeBudget(t *testing.T, out FailedCronsOut) int {
	t.Helper()
	count := len(out.Items)
	synthetic := out
	synthetic.Items = []evidence.Item{}
	synthetic.OmittedItems = count
	synthetic.ByteBudgetTruncated = true
	synthetic.Truncated = true
	synthetic.DeliveryComplete = false
	synthetic.TimeBasis = "unknown"
	synthetic.ByteSize = 999999
	return envelopeWireSize(t, synthetic) + 64
}

// failedCronsCampaignApp builds a Healthchecks fixture with current down checks
// and historical transitions, including flips after the collection time.
func failedCronsCampaignApp(t *testing.T, byteBudget, maxItems int) *App {
	t.Helper()
	t.Setenv("HC_CAMPAIGN_TOKEN", "fixture-token")
	now := time.Now().UTC().Truncate(time.Second)

	checks := []map[string]any{{
		"name": "nightly", "slug": "nightly", "status": "up",
		"unique_key": strings.Repeat("a", 32),
	}}
	for index := 0; index < 8; index++ {
		checks = append(checks, map[string]any{
			"name": fmt.Sprintf("watch-%d", index), "slug": fmt.Sprintf("watch-%d", index),
			"tags": "current", "status": "down", "unique_key": fmt.Sprintf("%032d", index),
		})
	}
	flips := make([]map[string]any, 0, 48)
	for index := 0; index < 40; index++ {
		at := now.Add(-23*time.Hour + time.Duration(index)*20*time.Minute)
		flips = append(flips, map[string]any{"timestamp": at.Format(time.RFC3339), "up": index % 2})
	}
	for index := 1; index <= 8; index++ {
		flips = append(flips, map[string]any{
			"timestamp": now.Add(time.Duration(index) * time.Minute).Format(time.RFC3339), "up": 0,
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/checks/":
			_ = json.NewEncoder(w).Encode(map[string]any{"checks": checks})
		case strings.HasPrefix(r.URL.Path, "/api/v3/checks/") && strings.HasSuffix(r.URL.Path, "/flips/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"flips": flips})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	raw := fmt.Sprintf(`version: 1
limits:
  max_cron_window: 24h
  max_evidence_items: %d
  max_incident_response_bytes: 8192
  source_cache_ttl: 0s
  per_source_timeout: 2s
  total_timeout: 10s
sources:
  hc:
    kind: healthchecks
    base_url: %s
    token_env: HC_CAMPAIGN_TOKEN
services:
  - id: hist
    sources:
      healthchecks: { source: hc, check_name: nightly, history_mode: transitions }
  - id: cur
    sources:
      healthchecks: { source: hc, check_tags: [current] }
`, maxItems, server.URL)
	cfg, err := config.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	app, err := NewApp(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), audit.New(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	app.Cfg.Limits.MaxIncidentBytes = byteBudget
	return app
}
