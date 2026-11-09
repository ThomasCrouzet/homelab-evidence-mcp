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
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/audit"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/config"
)

func TestDoctor_CapsHighCardinalityBindings(t *testing.T) {
	var active, peak atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		current := active.Add(1)
		for {
			old := peak.Load()
			if current <= old || peak.CompareAndSwap(old, current) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		active.Add(-1)
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"name": "api", "key": "api",
			"results": []map[string]any{{"success": true, "status": 200, "timestamp": time.Now().UTC().Format(time.RFC3339)}},
		}})
	}))
	defer server.Close()

	var builder strings.Builder
	builder.WriteString("version: 1\nlimits:\n  per_source_timeout: 2s\n  total_timeout: 30s\nsources:\n  gatus:\n    kind: gatus\n    base_url: ")
	builder.WriteString(server.URL)
	builder.WriteString("\nservices:\n")
	for index := 0; index < 250; index++ {
		fmt.Fprintf(&builder, "  - id: svc-%03d\n    sources:\n      gatus: { source: gatus, endpoint_key: api }\n", index)
	}
	cfg, err := config.Parse([]byte(builder.String()))
	if err != nil {
		t.Fatal(err)
	}
	app, err := NewApp(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), audit.New(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	report := app.Doctor(context.Background())
	if len(report.Sources) != 1 {
		t.Fatalf("sources=%d", len(report.Sources))
	}
	source := report.Sources[0]
	if len(source.Bindings) != maxDoctorBindings || source.OmittedBindings != 150 {
		t.Fatalf("bindings=%d omitted=%d", len(source.Bindings), source.OmittedBindings)
	}
	foundWarning := false
	for _, warning := range report.Warnings {
		if strings.Contains(warning, "more bindings") {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Fatalf("missing omission warning: %v", report.Warnings)
	}
	if peak.Load() > int32(maxConcurrentSourceRequests) {
		t.Fatalf("doctor peak concurrency=%d", peak.Load())
	}
}
