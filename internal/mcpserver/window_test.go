package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/config"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/evidence"
)

func TestParseWindow_RejectsAmbiguousAndFutureWindows(t *testing.T) {
	app := &App{}
	now := time.Now().UTC()
	cases := []struct {
		name    string
		start   string
		end     string
		dur     string
		wantErr bool
	}{
		{name: "start and duration conflict", start: now.Add(-time.Hour).Format(time.RFC3339), dur: "1h", wantErr: true},
		{name: "future start", start: now.Add(time.Hour).Format(time.RFC3339), wantErr: true},
		{name: "start before end", start: now.Add(-time.Hour).Format(time.RFC3339), end: now.Format(time.RFC3339)},
		{name: "duration with end", end: now.Format(time.RFC3339), dur: "30m"},
		{name: "start only", start: now.Add(-time.Hour).Format(time.RFC3339)},
		{name: "end only", end: now.Add(-time.Minute).Format(time.RFC3339)},
		{name: "oversized", start: now.Add(-48 * time.Hour).Format(time.RFC3339), end: now.Format(time.RFC3339), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := app.parseWindow(tc.start, tc.end, tc.dur, time.Hour, 24*time.Hour)
			if tc.wantErr && err == nil {
				t.Fatal("expected an error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func toolErrorBody(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	if res == nil || !res.IsError {
		t.Fatalf("expected a tool error, got %+v", res)
	}
	if len(res.Content) == 0 {
		t.Fatal("tool error has no content")
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content type %T", res.Content[0])
	}
	body := map[string]any{}
	if err := json.Unmarshal([]byte(text.Text), &body); err != nil {
		t.Fatalf("error body is not JSON: %v", err)
	}
	return body
}

func TestTools_RejectNegativeLimitsAndConflictingWindows(t *testing.T) {
	app := fullFixtureApp(t)
	checks := []struct {
		name string
		call func() (*mcp.CallToolResult, any, error)
	}{
		{name: "list offset", call: func() (*mcp.CallToolResult, any, error) {
			return app.toolListServices(context.Background(), nil, listServicesIn{Offset: -1})
		}},
		{name: "list limit", call: func() (*mcp.CallToolResult, any, error) {
			return app.toolListServices(context.Background(), nil, listServicesIn{Limit: -1})
		}},
		{name: "incident max items", call: func() (*mcp.CallToolResult, any, error) {
			return app.toolIncidentContext(context.Background(), nil, incidentIn{ServiceID: "media", MaxItems: -1})
		}},
		{name: "search limit", call: func() (*mcp.CallToolResult, any, error) {
			return app.toolSearchLogs(context.Background(), nil, searchLogsIn{ServiceID: "media", Limit: -1})
		}},
		{name: "start and duration", call: func() (*mcp.CallToolResult, any, error) {
			return app.toolIncidentContext(context.Background(), nil, incidentIn{
				ServiceID: "media", Start: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339), Duration: "30m",
			})
		}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			res, _, err := check.call()
			if err != nil {
				t.Fatal(err)
			}
			body := toolErrorBody(t, res)
			if body["reason"] != "invalid_arguments" {
				t.Fatalf("reason=%v", body["reason"])
			}
		})
	}
}

func TestLocalTools_RespectCancellation(t *testing.T) {
	app := fullFixtureApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, _, err := app.toolListServices(ctx, nil, listServicesIn{})
	if err != nil {
		t.Fatal(err)
	}
	if body := toolErrorBody(t, res); body["reason"] != "canceled" {
		t.Fatalf("cancellation reason=%v", body["reason"])
	}

	// The source populates the evidence cache first.
	if _, _, err := app.toolServiceStatus(context.Background(), nil, serviceIDIn{ServiceID: "media"}); err != nil {
		t.Fatal(err)
	}
	if app.Cache.Stats().Items == 0 {
		t.Fatal("fixture did not cache evidence")
	}
	res, _, err = app.toolGetEvidence(ctx, nil, getEvidenceIn{ID: "whatever"})
	if err != nil {
		t.Fatal(err)
	}
	if body := toolErrorBody(t, res); body["reason"] != "canceled" {
		t.Fatalf("canceled get_evidence reason=%v", body["reason"])
	}

	// A pre-canceled call must not consume the concurrent budget slot.
	app2 := fullFixtureApp(t)
	app2.budget = newToolBudget(100, 1)
	_, _, _ = app2.toolListServices(ctx, nil, listServicesIn{})
	if res, _, err := app2.toolListServices(context.Background(), nil, listServicesIn{}); err != nil || res.IsError {
		t.Fatalf("budget slot leaked after cancellation: %v %+v", err, res)
	}
}

func TestWithSourceTimeout_CanceledCallbackIsNotSuccess(t *testing.T) {
	app := &App{
		Cfg: &config.Config{Limits: config.Limits{PerSourceTimeout: time.Second}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	items := []evidence.Item{{ID: "ev", Source: evidence.SourceGatus, Freshness: evidence.FreshnessLive}}
	outcome, got := app.withSourceTimeout(ctx, "gatus", evidence.SourceGatus, func(context.Context) ([]evidence.Item, error) {
		cancel()
		return items, nil
	})
	if outcome.Status != "canceled" {
		t.Fatalf("status=%s reason=%s", outcome.Status, outcome.Reason)
	}
	if len(got) != 1 || outcome.ItemCount != 1 {
		t.Fatalf("partial evidence dropped: outcome=%+v items=%d", outcome, len(got))
	}
	if !strings.Contains(outcome.Error, "canceled") {
		t.Fatalf("cancellation error=%q", outcome.Error)
	}
}
