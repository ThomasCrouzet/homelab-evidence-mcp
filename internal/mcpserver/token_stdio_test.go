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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestStdioRedactsConfiguredTokensAndCachedEvidence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	binary := stdioTestBinary(t, ctx)
	for _, loader := range []string{"env", "file"} {
		t.Run(loader, func(t *testing.T) {
			credential := "fixture-credential-sunflower"
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+credential {
					t.Error("unexpected upstream method or authentication")
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				calls.Add(1)
				_ = json.NewEncoder(w).Encode([]map[string]any{{
					"key": "fixture", "name": credential,
					"results": []map[string]any{{"success": false, "status": 503, "timestamp": time.Now().UTC().Format(time.RFC3339), "errors": []string{credential}}},
				}})
			}))
			defer upstream.Close()
			dir := t.TempDir()
			auditPath := filepath.Join(dir, "audit.jsonl")
			auth := "token_env: HEM_FIXTURE_TOKEN"
			if loader == "file" {
				path := filepath.Join(dir, "credential")
				if err := os.WriteFile(path, []byte(credential), 0o600); err != nil {
					t.Fatal(err)
				}
				auth = "token_file: " + path
			}
			configPath := filepath.Join(dir, "config.yaml")
			configText := fmt.Sprintf("version: 1\nsources:\n  fixture:\n    kind: gatus\n    base_url: %s\n    %s\nservices:\n  - id: fixture\n    display_name: Fixture\n    sources:\n      gatus:\n        source: fixture\n        endpoint_key: fixture\n", upstream.URL, auth)
			configText += fmt.Sprintf("audit:\n  file: %s\nredact:\n  - exact: private-fixture-label\n  - exact: private-\"fixture-quoted-value\"-label\n", auditPath)
			if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
				t.Fatal(err)
			}
			command := exec.CommandContext(ctx, binary, "--config", configPath)
			var stderr bytes.Buffer
			command.Stderr = &stderr
			command.Env = append(os.Environ(), "HEM_FIXTURE_TOKEN="+credential)
			client := mcp.NewClient(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
			session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command, TerminateDuration: stdioShutdownTimeout}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = session.Close() }()
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "service_status", Arguments: map[string]any{"service_id": "fixture"}})
			if err != nil || result.IsError {
				t.Fatalf("service_status failed: %v", err)
			}
			body, err := json.Marshal(result.StructuredContent)
			if err != nil {
				t.Fatal(err)
			}
			var response struct {
				Items []struct{ ID string } `json:"items"`
			}
			if err := json.Unmarshal(body, &response); err != nil || len(response.Items) != 1 || calls.Load() != 1 {
				t.Fatalf("expected one authenticated fixture result: %v", err)
			}
			encoded, err := json.Marshal(result)
			if err != nil || strings.Contains(string(encoded), credential) || !strings.Contains(string(encoded), "[REDACTED]") {
				t.Fatal("configured credential was not redacted from stdio output")
			}
			t.Logf("service_status: %s", encoded)
			cached, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_evidence", Arguments: map[string]any{"id": response.Items[0].ID}})
			if err != nil || cached.IsError {
				t.Fatalf("get_evidence failed: %v", err)
			}
			encoded, err = json.Marshal(cached)
			if err != nil || strings.Contains(string(encoded), credential) || !strings.Contains(string(encoded), "[REDACTED]") {
				t.Fatal("configured credential was not redacted from cached stdio output")
			}
			t.Logf("get_evidence: %s", encoded)

			secrets := []string{credential, "private-fixture-label", "private-\"fixture-quoted-value\"-label"}
			for _, secret := range secrets {
				for _, tool := range []string{"service_status", "incident_context", "search_logs", "failed_crons"} {
					failed, err := session.CallTool(ctx, &mcp.CallToolParams{
						Name: tool, Arguments: map[string]any{"service_id": secret},
					})
					if err != nil || failed == nil || !failed.IsError {
						t.Fatalf("%s must reject an unknown service: %v", tool, err)
					}
					body, err := json.Marshal(failed)
					if err != nil || strings.Contains(string(body), secret) || strings.Contains(string(body), "fixture-quoted-value") {
						t.Errorf("%s did not redact an error response", tool)
					}
				}
				failed, err := session.CallTool(ctx, &mcp.CallToolParams{
					Name: "failed_crons", Arguments: map[string]any{"service_id": secret, "duration": "invalid"},
				})
				if err != nil || failed == nil || !failed.IsError {
					t.Fatalf("failed_crons must reject an invalid duration: %v", err)
				}
			}
			failed, err := session.CallTool(ctx, &mcp.CallToolParams{
				Name: "service_status", Arguments: map[string]any{"service_id": strings.Repeat("x", 20000)},
			})
			if err != nil || failed == nil || !failed.IsError {
				t.Fatalf("service_status must reject an oversized service identifier: %v", err)
			}
			globalCrons, err := session.CallTool(ctx, &mcp.CallToolParams{
				Name: "failed_crons", Arguments: map[string]any{"service_id": strings.Repeat(" ", 20000)},
			})
			if err != nil || globalCrons == nil || globalCrons.IsError {
				t.Fatalf("failed_crons must accept an empty service filter: %v", err)
			}
			if err := session.Close(); err != nil {
				t.Fatal(err)
			}
			auditLog, err := os.ReadFile(auditPath)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range append(secrets, "fixture-quoted-value") {
				if strings.Contains(stderr.String(), secret) || bytes.Contains(auditLog, []byte(secret)) {
					t.Error("error audit output contains a configured secret")
				}
			}
			for _, line := range bytes.Split(bytes.TrimSpace(auditLog), []byte("\n")) {
				var event struct {
					ServiceID string `json:"service_id"`
				}
				if err := json.Unmarshal(line, &event); err != nil {
					t.Fatalf("invalid audit JSON: %v", err)
				}
				if len([]rune(event.ServiceID)) > 128 {
					t.Error("audit service identifier exceeds its size limit")
				}
			}
			t.Logf("error responses and audit output checked for %s credentials", loader)
		})
	}
}
