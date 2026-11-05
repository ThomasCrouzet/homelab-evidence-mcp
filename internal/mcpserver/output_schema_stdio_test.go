package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// This test detects missing output contracts, wire results that do not match
// their advertised schema, and text/structured result divergence.
func TestStdioOutputSchemas(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	t0 := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"key": "schema-app", "name": "schema-app",
			"results": []map[string]any{{
				"success": true, "status": 200, "timestamp": t0.Format(time.RFC3339),
			}},
		}})
	}))
	defer upstream.Close()
	lokiUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"resultType": "streams",
				"result": []map[string]any{{
					"stream": map[string]string{"container": "schema-app"},
					"values": [][]string{{strconv.FormatInt(t0.UnixNano(), 10), "level=info schema fixture"}},
				}},
			},
		})
	}))
	defer lokiUpstream.Close()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configText := `version: 1
limits:
  default_window: 15m
sources:
  gatus:
    kind: gatus
    base_url: ` + upstream.URL + `
  loki:
    kind: loki
    base_url: ` + lokiUpstream.URL + `
services:
  - id: schema-service
    sources:
      gatus: { source: gatus, endpoint_key: schema-app }
      loki: { source: loki, selector: '{container="schema-app"}' }
`
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}

	binary := stdioTestBinary(t, ctx)
	var stderr bytes.Buffer
	command := exec.CommandContext(ctx, binary, "--config", configPath)
	command.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "schema-fixture", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command, TerminateDuration: stdioShutdownTimeout}, nil)
	if err != nil {
		t.Fatalf("connect to executable: %v\nstderr:\n%s", err, stderr.String())
	}
	defer func() { _ = session.Close() }()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	schemas := make(map[string]*jsonschema.Resolved, len(tools.Tools))
	schemaJSON := make(map[string][]byte, len(tools.Tools))
	for _, tool := range tools.Tools {
		if tool.OutputSchema == nil {
			t.Fatalf("tool %s does not publish an output schema", tool.Name)
		}
		raw, err := json.Marshal(tool.OutputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema jsonschema.Schema
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("%s schema: %v", tool.Name, err)
		}
		resolved, err := schema.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true})
		if err != nil {
			t.Fatalf("%s schema resolve: %v", tool.Name, err)
		}
		schemas[tool.Name] = resolved
		schemaJSON[tool.Name] = raw
	}
	for _, vocabulary := range []string{`"severity"`, `"freshness"`, `"source"`, `"kind"`, `"status"`} {
		found := false
		for _, raw := range schemaJSON {
			if bytes.Contains(raw, []byte(vocabulary)) {
				found = true
			}
		}
		if !found {
			t.Fatalf("schemas do not describe %s", vocabulary)
		}
	}

	call := func(name string, arguments map[string]any) *mcp.CallToolResult {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if result.IsError {
			t.Fatalf("%s returned an error: %s", name, mustJSON(t, result))
		}
		if result.StructuredContent == nil {
			t.Fatalf("%s returned no structured content", name)
		}
		validateWireResult(t, name, result, schemas[name])
		return result
	}

	call("evidence_capabilities", nil)
	call("list_services", map[string]any{})
	status := call("service_status", map[string]any{"service_id": "schema-service"})
	call("incident_context", map[string]any{"service_id": "schema-service"})
	call("search_logs", map[string]any{"service_id": "schema-service"})
	call("failed_crons", map[string]any{})

	var statusBody struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	executableE2EDecode(t, structuredBytes(t, status), &statusBody)
	if len(statusBody.Items) == 0 {
		t.Fatal("service_status returned no item id")
	}
	call("get_evidence", map[string]any{"id": statusBody.Items[0].ID})

	empty, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "failed_crons", Arguments: map[string]any{},
	})
	if err != nil || empty.IsError {
		t.Fatalf("empty failed_crons failed: %v", err)
	}
	validateWireResult(t, "failed_crons", empty, schemas["failed_crons"])
	var emptyBody map[string]json.RawMessage
	if err := json.Unmarshal(structuredBytes(t, empty), &emptyBody); err != nil {
		t.Fatal(err)
	}
	if string(emptyBody["items"]) != "[]" {
		t.Fatalf("empty collection is not an array: %s", emptyBody["items"])
	}

	failed, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "service_status", Arguments: map[string]any{"service_id": "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !failed.IsError || failed.StructuredContent != nil {
		t.Fatalf("error result has a successful output envelope: %s", mustJSON(t, failed))
	}
}

func structuredBytes(t *testing.T, result *mcp.CallToolResult) []byte {
	t.Helper()
	switch value := result.StructuredContent.(type) {
	case json.RawMessage:
		return value
	case []byte:
		return value
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
}

func validateWireResult(t *testing.T, name string, result *mcp.CallToolResult, schema *jsonschema.Resolved) {
	t.Helper()
	if schema == nil {
		t.Fatalf("%s has no schema", name)
	}
	var structured any
	if err := json.Unmarshal(structuredBytes(t, result), &structured); err != nil {
		t.Fatalf("%s structured content: %v", name, err)
	}
	if err := schema.Validate(structured); err != nil {
		t.Fatalf("%s structured content does not match its schema: %v", name, err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("%s content count=%d", name, len(result.Content))
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("%s content type=%T", name, result.Content[0])
	}
	var textValue any
	if err := json.Unmarshal([]byte(text.Text), &textValue); err != nil {
		t.Fatalf("%s text content: %v", name, err)
	}
	if !reflect.DeepEqual(textValue, structured) {
		t.Fatalf("%s text and structured content differ", name)
	}
}
