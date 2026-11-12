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

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/audit"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/config"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/evidence"
)

// documentedToolErrorReasons is the closed set of request rejection reasons
// that the README and docs/tool-output-schemas.md advertise.
var documentedToolErrorReasons = []string{
	"invalid_arguments",
	"unknown_service",
	"binding_missing",
	"evidence_not_found",
	"response_budget_exceeded",
	"canceled",
	"concurrent_tool_limit",
	"tool_rate_limit",
	"tool_budget_exhausted",
}

// TestToolErrorReasons detects rejections that force a client to parse English
// text to learn why a call failed.
func TestToolErrorReasons(t *testing.T) {
	app := toolErrorReasonsApp(t)

	cases := []struct {
		name   string
		reason string
		call   func() (*mcp.CallToolResult, any, error)
	}{
		{name: "oversized prefix", reason: "invalid_arguments", call: func() (*mcp.CallToolResult, any, error) {
			return app.toolListServices(context.Background(), nil, listServicesIn{Prefix: strings.Repeat("p", 129)})
		}},
		{name: "empty service id", reason: "invalid_arguments", call: func() (*mcp.CallToolResult, any, error) {
			return app.toolServiceStatus(context.Background(), nil, serviceIDIn{ServiceID: "  "})
		}},
		{name: "empty evidence id", reason: "invalid_arguments", call: func() (*mcp.CallToolResult, any, error) {
			return app.toolGetEvidence(context.Background(), nil, getEvidenceIn{ID: ""})
		}},
		{name: "invalid window", reason: "invalid_arguments", call: func() (*mcp.CallToolResult, any, error) {
			return app.toolIncidentContext(context.Background(), nil, incidentIn{
				ServiceID: "logs", Start: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339), Duration: "30m",
			})
		}},
		{name: "unknown service", reason: "unknown_service", call: func() (*mcp.CallToolResult, any, error) {
			return app.toolServiceStatus(context.Background(), nil, serviceIDIn{ServiceID: "missing"})
		}},
		{name: "unknown service in logs", reason: "unknown_service", call: func() (*mcp.CallToolResult, any, error) {
			return app.toolSearchLogs(context.Background(), nil, searchLogsIn{ServiceID: "missing"})
		}},
		{name: "unknown service in crons", reason: "unknown_service", call: func() (*mcp.CallToolResult, any, error) {
			return app.toolFailedCrons(context.Background(), nil, failedCronsIn{ServiceID: "missing"})
		}},
		{name: "missing loki binding", reason: "binding_missing", call: func() (*mcp.CallToolResult, any, error) {
			return app.toolSearchLogs(context.Background(), nil, searchLogsIn{ServiceID: "web"})
		}},
		{name: "missing healthchecks binding", reason: "binding_missing", call: func() (*mcp.CallToolResult, any, error) {
			return app.toolFailedCrons(context.Background(), nil, failedCronsIn{ServiceID: "logs"})
		}},
		{name: "unknown evidence id", reason: "evidence_not_found", call: func() (*mcp.CallToolResult, any, error) {
			return app.toolGetEvidence(context.Background(), nil, getEvidenceIn{ID: "expired"})
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			res, _, err := testCase.call()
			if err != nil {
				t.Fatal(err)
			}
			assertToolErrorReason(t, res, testCase.reason)
		})
	}

	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		res, _, err := app.toolListServices(ctx, nil, listServicesIn{})
		if err != nil {
			t.Fatal(err)
		}
		assertToolErrorReason(t, res, "canceled")
	})

	t.Run("concurrent_tool_limit", func(t *testing.T) {
		limited := &App{budget: newToolBudget(1000, 1)}
		started := make(chan struct{})
		release := make(chan struct{})
		go func() {
			_, _, _ = limited.withBudget(context.Background(), "service_status", func() (*mcp.CallToolResult, any, error) {
				close(started)
				<-release
				return textResult(map[string]string{"ok": "held"}), nil, nil
			})
		}()
		<-started
		res, _, err := limited.withBudget(context.Background(), "service_status", func() (*mcp.CallToolResult, any, error) {
			t.Fatal("a second call must not run under a concurrent budget of one")
			return nil, nil, nil
		})
		close(release)
		if err != nil {
			t.Fatal(err)
		}
		assertToolErrorReason(t, res, "concurrent_tool_limit")
	})

	t.Run("tool_rate_limit", func(t *testing.T) {
		limited := &App{budget: newToolBudget(1, 8)}
		if res, _, err := limited.withBudget(context.Background(), "list_services", func() (*mcp.CallToolResult, any, error) {
			return textResult(map[string]string{"ok": "first"}), nil, nil
		}); err != nil || res.IsError {
			t.Fatalf("first call: %v %+v", err, res)
		}
		res, _, err := limited.withBudget(context.Background(), "list_services", func() (*mcp.CallToolResult, any, error) {
			t.Fatal("a call above the per-minute budget must not run")
			return nil, nil, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		assertToolErrorReason(t, res, "tool_rate_limit")
	})
}

// TestToolResponseBudgetReason detects a response budget failure without a
// stable reason.
func TestToolResponseBudgetReason(t *testing.T) {
	t.Run("metadata", func(t *testing.T) {
		app := healthchecksBulkApp(t)
		app.Cfg.Limits.MaxIncidentBytes = 600
		for _, call := range []func() (*mcp.CallToolResult, any, error){
			func() (*mcp.CallToolResult, any, error) {
				return app.toolServiceStatus(context.Background(), nil, serviceIDIn{ServiceID: "crons"})
			},
			func() (*mcp.CallToolResult, any, error) {
				return app.toolFailedCrons(context.Background(), nil, failedCronsIn{Duration: "1h"})
			},
		} {
			res, _, err := call()
			if err != nil {
				t.Fatal(err)
			}
			assertToolErrorReason(t, res, "response_budget_exceeded")
		}
	})

	t.Run("evidence item", func(t *testing.T) {
		app := budgetApp(t, lokiBulkServer(t).URL)
		app.Cache.Put(evidence.Item{
			ID: "oversized", Source: evidence.SourceLoki, SourceID: "oversized",
			ObservedAt: time.Now().UTC(), RetrievedAt: time.Now().UTC(),
			Summary: "oversized", Attributes: map[string]any{"line": strings.Repeat("x", 20000)},
		})
		res, _, err := app.toolGetEvidence(context.Background(), nil, getEvidenceIn{ID: "oversized"})
		if err != nil {
			t.Fatal(err)
		}
		assertToolErrorReason(t, res, "response_budget_exceeded")
	})
}

func assertToolErrorReason(t *testing.T, res *mcp.CallToolResult, want string) {
	t.Helper()
	body := toolErrorBody(t, res)
	if body["reason"] != want {
		t.Fatalf("reason=%v, want %s: %v", body["reason"], want, body)
	}
	if !containsString(documentedToolErrorReasons, want) {
		t.Fatalf("reason %q is outside the documented set", want)
	}
	message, _ := body["error"].(string)
	if strings.TrimSpace(message) == "" {
		t.Fatalf("error has no readable message: %v", body)
	}
	if res.StructuredContent != nil {
		t.Fatalf("error result carries structured content")
	}
}

// toolErrorReasonsApp builds one Loki service and one Gatus-only service.
func toolErrorReasonsApp(t *testing.T) *App {
	t.Helper()
	loki := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data":   map[string]any{"resultType": "streams", "result": []map[string]any{}},
		})
	}))
	t.Cleanup(loki.Close)
	gatus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"name": "web", "key": "web",
			"results": []map[string]any{{"success": true, "status": 200, "timestamp": time.Now().UTC().Format(time.RFC3339)}},
		}})
	}))
	t.Cleanup(gatus.Close)

	raw := fmt.Sprintf(`version: 1
limits:
  max_incident_response_bytes: 8192
  source_cache_ttl: 0s
sources:
  loki: { kind: loki, base_url: %s }
  gatus: { kind: gatus, base_url: %s }
services:
  - id: logs
    sources:
      loki: { source: loki, selector: '{container="app"}' }
  - id: web
    sources:
      gatus: { source: gatus, endpoint_key: web }
`, loki.URL, gatus.URL)
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
