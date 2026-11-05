package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// This test detects alias masking, row-order dependence, and silent ambiguity
// in Gatus endpoint selection. Fixtures are synthetic.
func TestStdioGatusEndpointPrecedence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	t0 := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	aliasRow := fmt.Sprintf(
		`{"name":"prod_api","group":"dev","key":"dev_prod_api","results":[{"success":true,"status":200,"timestamp":%q}]}`,
		t0.Format(time.RFC3339),
	)
	exactRow := fmt.Sprintf(
		`{"name":"api","group":"prod","key":"prod_api","results":[{"success":false,"status":503,"timestamp":%q}]}`,
		t0.Format(time.RFC3339),
	)

	servers := map[string]string{}
	for name, body := range map[string]string{
		"ordered":  "[" + aliasRow + "," + exactRow + "]",
		"reversed": "[" + exactRow + "," + aliasRow + "]",
		"duplicate": "[" +
			fmt.Sprintf(`{"name":"one","key":"prod_api","results":[{"success":true,"status":200,"timestamp":%q}]}`, t0.Format(time.RFC3339)) + "," +
			fmt.Sprintf(`{"name":"two","key":"prod_api","results":[{"success":false,"status":503,"timestamp":%q}]}`, t0.Format(time.RFC3339)) + "]",
		"alias": fmt.Sprintf(
			`[{"name":"api","group":"prod","key":"legacy_key","results":[{"success":false,"status":503,"timestamp":%q}]}]`,
			t0.Format(time.RFC3339),
		),
	} {
		body := body
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/api/v1/endpoints/statuses" {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(server.Close)
		servers[name] = server.URL
	}

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configText := fmt.Sprintf(`version: 1
limits:
  default_window: 15m
  max_incident_window: 1h
sources:
  gatus-ordered:
    kind: gatus
    base_url: %s
  gatus-reversed:
    kind: gatus
    base_url: %s
  gatus-duplicate:
    kind: gatus
    base_url: %s
  gatus-alias:
    kind: gatus
    base_url: %s
services:
  - id: ordered
    sources:
      gatus: { source: gatus-ordered, endpoint_key: prod_api }
  - id: reversed
    sources:
      gatus: { source: gatus-reversed, endpoint_key: prod_api }
  - id: duplicate
    sources:
      gatus: { source: gatus-duplicate, endpoint_key: prod_api }
  - id: alias
    sources:
      gatus: { source: gatus-alias, endpoint_key: prod_api }
`, servers["ordered"], servers["reversed"], servers["duplicate"], servers["alias"])
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}

	binary := stdioTestBinary(t, ctx)
	var stderr bytes.Buffer
	command := exec.CommandContext(ctx, binary, "--config", configPath)
	command.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "gatus-fixture", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command, TerminateDuration: stdioShutdownTimeout}, nil)
	if err != nil {
		t.Fatalf("connect to executable: %v\nstderr:\n%s", err, stderr.String())
	}
	defer func() { _ = session.Close() }()

	for _, service := range []string{"ordered", "reversed", "alias"} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name: "service_status", Arguments: map[string]any{"service_id": service},
		})
		if err != nil || result.IsError {
			t.Fatalf("%s service_status failed: %v\n%s", service, err, stderr.String())
		}
		var body struct {
			Items []struct {
				Attributes map[string]any `json:"attributes"`
			} `json:"items"`
		}
		executableE2EDecode(t, mustJSON(t, result.StructuredContent), &body)
		if len(body.Items) != 1 || body.Items[0].Attributes["http_status"] != float64(503) {
			t.Fatalf("%s selected the wrong endpoint: %s", service, mustJSON(t, result.StructuredContent))
		}
		if service != "alias" && body.Items[0].Attributes["endpoint_key"] != "prod_api" {
			t.Fatalf("%s selected a non-exact endpoint: %s", service, mustJSON(t, result.StructuredContent))
		}
	}

	duplicate, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "service_status", Arguments: map[string]any{"service_id": "duplicate"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var duplicateBody struct {
		Items   []json.RawMessage `json:"items"`
		Sources []struct {
			Kind   string `json:"kind"`
			Status string `json:"status"`
			Error  string `json:"error"`
		} `json:"sources"`
	}
	executableE2EDecode(t, mustJSON(t, duplicate.StructuredContent), &duplicateBody)
	ambiguous := false
	for _, source := range duplicateBody.Sources {
		if source.Kind == "gatus" && source.Status == "error" &&
			bytes.Contains([]byte(source.Error), []byte("ambiguous")) {
			ambiguous = true
		}
	}
	if len(duplicateBody.Items) != 0 || !ambiguous {
		t.Fatalf("duplicate exact keys were not reported as ambiguous: %s", mustJSON(t, duplicate.StructuredContent))
	}

	windowStart := t0.Add(-time.Minute).Format(time.RFC3339)
	windowEnd := t0.Add(time.Minute).Format(time.RFC3339)
	for _, service := range []string{"ordered", "reversed"} {
		incident, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name: "incident_context", Arguments: map[string]any{
				"service_id": service, "start": windowStart, "end": windowEnd,
			},
		})
		if err != nil || incident.IsError {
			t.Fatalf("%s incident_context failed: %v", service, err)
		}
		var body struct {
			Items []struct {
				Source     string         `json:"source"`
				Attributes map[string]any `json:"attributes"`
			} `json:"items"`
		}
		executableE2EDecode(t, mustJSON(t, incident.StructuredContent), &body)
		found := false
		for _, item := range body.Items {
			if item.Source != "gatus" {
				continue
			}
			found = true
			if item.Attributes["http_status"] != float64(503) || item.Attributes["endpoint_key"] != "prod_api" {
				t.Fatalf("%s window used an alias: %s", service, mustJSON(t, incident.StructuredContent))
			}
		}
		if !found {
			t.Fatalf("%s window returned no gatus evidence: %s", service, mustJSON(t, incident.StructuredContent))
		}
	}
}
