package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/audit"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/config"
)

func TestNoBoundSourcesIsNotComplete(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()
	raw := fmt.Sprintf(`version: 1
limits:
  max_cron_window: 24h
sources:
  gatus:
    kind: gatus
    base_url: %s
services:
  - id: empty
    display_name: Empty
`, server.URL)
	cfg, err := config.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	app, err := NewApp(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), audit.New(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	_, status, err := app.toolServiceStatus(context.Background(), nil, serviceIDIn{ServiceID: "empty"})
	if err != nil {
		t.Fatal(err)
	}
	statusBody := status.(ServiceStatusOut)
	if statusBody.Complete || statusBody.Reason != "no_bound_sources" {
		t.Fatalf("service_status=%+v", statusBody)
	}
	_, incident, err := app.toolIncidentContext(context.Background(), nil, incidentIn{ServiceID: "empty"})
	if err != nil {
		t.Fatal(err)
	}
	incidentBody := incident.(IncidentContextOut)
	if incidentBody.Complete || incidentBody.Reason != "no_bound_sources" {
		t.Fatalf("incident=%+v", incidentBody)
	}
	_, crons, err := app.toolFailedCrons(context.Background(), nil, failedCronsIn{Duration: "1h"})
	if err != nil {
		t.Fatal(err)
	}
	cronBody := crons.(FailedCronsOut)
	if cronBody.Complete || cronBody.Reason != "no_bound_sources" {
		t.Fatalf("failed_crons=%+v", cronBody)
	}
	rawJSON, _ := json.Marshal(cronBody)
	if len(rawJSON) == 0 {
		t.Fatal("empty output")
	}
}
