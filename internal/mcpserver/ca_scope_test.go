package mcpserver

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/audit"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/config"
)

func gatusTLSFixture(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"name": "api", "key": "api",
			"results": []map[string]any{{"success": true, "status": 200, "timestamp": time.Now().UTC().Format(time.RFC3339)}},
		}})
	}))
	t.Cleanup(server.Close)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	path := filepath.Join(t.TempDir(), "source-ca.pem")
	if err := os.WriteFile(path, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return server, path
}

func TestPerSourceCAIsScoped(t *testing.T) {
	trusted, caPath := gatusTLSFixture(t)
	untrusted, _ := gatusTLSFixture(t)
	raw := fmt.Sprintf(`version: 1
limits:
  per_source_timeout: 2s
  total_timeout: 5s
sources:
  trusted:
    kind: gatus
    base_url: %s
    ca_file: %s
  untrusted:
    kind: gatus
    base_url: %s
services:
  - id: trusted
    sources:
      gatus: { source: trusted, endpoint_key: api }
  - id: untrusted
    sources:
      gatus: { source: untrusted, endpoint_key: api }
`, trusted.URL, caPath, untrusted.URL)
	cfg, err := config.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	app, err := NewApp(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), audit.New(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	okOutcome, _ := app.collectGatusStatus(context.Background(), "trusted", cfg.Services[0].Sources.Gatus)
	if okOutcome.Status != "ok" {
		t.Fatalf("per-source CA did not trust its source: %+v", okOutcome)
	}
	badOutcome, _ := app.collectGatusStatus(context.Background(), "untrusted", cfg.Services[1].Sources.Gatus)
	if badOutcome.Status == "ok" {
		t.Fatalf("untrusted certificate was accepted: %+v", badOutcome)
	}
}

func TestPerSourceCAMissingFileFailsStartup(t *testing.T) {
	raw := `version: 1
sources:
  gatus:
    kind: gatus
    base_url: https://gatus.example.internal
    ca_file: /missing/private-ca.pem
services: []
`
	cfg, err := config.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewApp(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), audit.New(io.Discard)); err == nil {
		t.Fatal("a missing per-source CA file was accepted")
	}
}
