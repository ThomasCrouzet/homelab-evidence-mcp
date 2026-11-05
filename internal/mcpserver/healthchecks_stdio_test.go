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

// This test detects read-only Healthchecks identity failures, silent empty
// selections, and identity disclosure. Fixtures use synthetic keys.
func TestStdioHealthchecksReadOnlyIdentity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	const (
		uniqueKey = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		uuid      = "11111111-1111-1111-1111-111111111111"
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v3/checks/" || r.Header.Get("X-Api-Key") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"checks": []map[string]any{
			{
				"name": "backup", "slug": "backup", "status": "down", "grace": 60,
				"unique_key": uniqueKey, "last_ping": time.Now().UTC().Add(-time.Minute).Format(time.RFC3339),
				"ping_url": "https://hc.example/ping/ping-secret-orchid",
			},
			{
				"name": "legacy", "slug": "legacy", "status": "down", "grace": 60,
				"uuid": uuid, "last_ping": time.Now().UTC().Add(-time.Minute).Format(time.RFC3339),
				"ping_url": "https://hc.example/ping/ping-secret-cedar",
			},
		}})
	}))
	defer upstream.Close()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configText := fmt.Sprintf(`version: 1
sources:
  hc:
    kind: healthchecks
    base_url: %s
    token_env: HEM_HC_TOKEN
services:
  - id: readonly
    sources:
      healthchecks:
        source: hc
        check_unique_key: %s
  - id: unknown
    sources:
      healthchecks:
        source: hc
        check_unique_key: cccccccccccccccccccccccccccccccc
  - id: legacy
    sources:
      healthchecks:
        source: hc
        check_uuid: %s
`, upstream.URL, uniqueKey, uuid)
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}

	binary := stdioTestBinary(t, ctx)
	var stderr bytes.Buffer
	command := exec.CommandContext(ctx, binary, "--config", configPath)
	command.Env = append(os.Environ(), "HEM_HC_TOKEN=fixture-readonly-token")
	command.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "healthchecks-fixture", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command, TerminateDuration: stdioShutdownTimeout}, nil)
	if err != nil {
		t.Fatalf("connect to executable: %v\nstderr:\n%s", err, stderr.String())
	}
	defer func() { _ = session.Close() }()

	readonly, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "service_status", Arguments: map[string]any{"service_id": "readonly"},
	})
	if err != nil || readonly.IsError {
		t.Fatalf("read-only selection failed: %v\n%s", err, stderr.String())
	}
	readonlyRaw, _ := json.Marshal(readonly)
	for _, identity := range []string{uniqueKey, uuid, "ping-secret-orchid", "fixture-readonly-token"} {
		if bytes.Contains(readonlyRaw, []byte(identity)) {
			t.Fatalf("response disclosed %q: %s", identity, readonlyRaw)
		}
	}
	var readonlyBody struct {
		Items []struct {
			Source     string `json:"source"`
			Severity   string `json:"severity"`
			Attributes struct {
				Status string `json:"status"`
			} `json:"attributes"`
		} `json:"items"`
	}
	executableE2EDecode(t, mustJSON(t, readonly.StructuredContent), &readonlyBody)
	if len(readonlyBody.Items) != 1 || readonlyBody.Items[0].Severity != "error" ||
		readonlyBody.Items[0].Attributes.Status != "down" {
		t.Fatalf("read-only unique key did not select the down check: %s", readonlyRaw)
	}

	unknown, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "service_status", Arguments: map[string]any{"service_id": "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if unknown.IsError {
		t.Fatalf("partial result must keep a successful MCP call: %s", mustJSON(t, unknown))
	}
	var unknownBody struct {
		Items   []json.RawMessage `json:"items"`
		Sources []struct {
			Kind   string `json:"kind"`
			Status string `json:"status"`
			Error  string `json:"error"`
		} `json:"sources"`
	}
	executableE2EDecode(t, mustJSON(t, unknown.StructuredContent), &unknownBody)
	if len(unknownBody.Items) != 0 {
		t.Fatalf("unknown binding returned items: %s", mustJSON(t, unknown))
	}
	foundExplicit := false
	for _, source := range unknownBody.Sources {
		if source.Kind == "healthchecks" && source.Status == "error" &&
			bytes.Contains([]byte(source.Error), []byte("matched no check")) {
			foundExplicit = true
		}
	}
	if !foundExplicit {
		t.Fatalf("unknown identity looked like a successful empty result: %s", mustJSON(t, unknown))
	}
	if bytes.Contains(mustJSON(t, unknown), []byte("cccccccc")) {
		t.Fatal("diagnostic disclosed the unknown identity")
	}

	legacy, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "service_status", Arguments: map[string]any{"service_id": "legacy"},
	})
	if err != nil || legacy.IsError {
		t.Fatalf("legacy UUID binding failed: %v", err)
	}
	var legacyBody struct {
		Items []struct {
			Severity string `json:"severity"`
		} `json:"items"`
	}
	executableE2EDecode(t, mustJSON(t, legacy.StructuredContent), &legacyBody)
	if len(legacyBody.Items) != 1 || legacyBody.Items[0].Severity != "error" {
		t.Fatalf("legacy UUID binding did not select the down check: %s", mustJSON(t, legacy))
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
