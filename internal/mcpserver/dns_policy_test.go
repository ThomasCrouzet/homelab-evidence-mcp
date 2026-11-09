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
	"time"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/audit"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/config"
)

func TestPerSourceAddressPolicy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"name": "api", "key": "api",
			"results": []map[string]any{{"success": true, "status": 200, "timestamp": time.Now().UTC().Format(time.RFC3339)}},
		}})
	}))
	defer server.Close()
	raw := fmt.Sprintf(`version: 1
limits:
  per_source_timeout: 2s
  total_timeout: 5s
sources:
  allowed:
    kind: gatus
    base_url: %s
    allowed_cidrs: ["127.0.0.0/8"]
  blocked:
    kind: gatus
    base_url: %s
    allowed_cidrs: ["192.0.2.0/24"]
services:
  - id: allowed
    sources:
      gatus: { source: allowed, endpoint_key: api }
  - id: blocked
    sources:
      gatus: { source: blocked, endpoint_key: api }
`, server.URL, server.URL)
	cfg, err := config.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	app, err := NewApp(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), audit.New(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	okOutcome, _ := app.collectGatusStatus(context.Background(), "allowed", cfg.Services[0].Sources.Gatus)
	if okOutcome.Status != "ok" {
		t.Fatalf("allowed address failed: %+v", okOutcome)
	}
	blockedOutcome, _ := app.collectGatusStatus(context.Background(), "blocked", cfg.Services[1].Sources.Gatus)
	if blockedOutcome.Status == "ok" {
		t.Fatalf("dial outside the allowed network was accepted: %+v", blockedOutcome)
	}
}
