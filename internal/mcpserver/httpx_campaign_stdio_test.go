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
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type httpxCampaignSnapshot struct {
	ObservedAt time.Time `json:"observed_at"`
}

func TestStdioHttpxCampaignCacheDirectives(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	binary := stdioTestBinary(t, ctx)

	cases := []struct {
		name   string
		header map[string]string
	}{
		{
			name: "pragma no-cache overrides max-age",
			header: map[string]string{
				"Cache-Control": "max-age=60",
				"Pragma":        "no-cache",
			},
		},
		{
			name: "exhausted upstream age skips storage",
			header: map[string]string{
				"Cache-Control": "max-age=60",
				"Age":           "60",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			base := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/endpoints/statuses" {
					http.NotFound(w, r)
					return
				}
				n := int(calls.Add(1))
				for name, value := range tc.header {
					w.Header().Set(name, value)
				}
				_ = json.NewEncoder(w).Encode([]map[string]any{{
					"name": "Campaign endpoint", "group": "campaign", "key": "campaign-app",
					"results": []map[string]any{{
						"success":   true,
						"status":    200,
						"timestamp": base.Add(time.Duration(n) * time.Second).Format(time.RFC3339),
					}},
				}})
			}))
			defer upstream.Close()

			configPath := filepath.Join(t.TempDir(), "config.yaml")
			configText := fmt.Sprintf(`version: 1
limits:
  source_cache_ttl: 1m
  per_source_timeout: 2s
  total_timeout: 10s
sources:
  gatus:
    kind: gatus
    base_url: %s
services:
  - id: campaign
    sources:
      gatus: { source: gatus, endpoint_key: campaign-app }
`, upstream.URL)
			if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
				t.Fatal(err)
			}

			var stderr bytes.Buffer
			command := exec.CommandContext(ctx, binary, "--config", configPath)
			command.Stderr = &stderr
			client := mcp.NewClient(&mcp.Implementation{Name: "httpx-campaign-stdio", Version: "1"}, nil)
			session, err := client.Connect(ctx, &mcp.CommandTransport{
				Command:           command,
				TerminateDuration: stdioShutdownTimeout,
			}, nil)
			if err != nil {
				t.Fatalf("connect to executable: %v\nstderr:\n%s", err, stderr.String())
			}
			defer func() { _ = session.Close() }()

			first := httpxCampaignCallStatus(t, ctx, session)
			second := httpxCampaignCallStatus(t, ctx, session)
			if got := calls.Load(); got != 2 {
				t.Fatalf("upstream calls=%d, want 2 with no reusable source cache entry", got)
			}
			if first.ObservedAt.Equal(second.ObservedAt) {
				t.Fatalf("second read reused the first snapshot: %s", first.ObservedAt)
			}
		})
	}
}

func httpxCampaignCallStatus(t *testing.T, ctx context.Context, session *mcp.ClientSession) httpxCampaignSnapshot {
	t.Helper()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "service_status", Arguments: map[string]any{"service_id": "campaign"},
	})
	if err != nil || result.IsError {
		t.Fatalf("service_status: %v %+v", err, result)
	}
	var output struct {
		Items []httpxCampaignSnapshot `json:"items"`
	}
	executableE2EDecode(t, mustJSON(t, result.StructuredContent), &output)
	if len(output.Items) != 1 {
		t.Fatalf("items=%d: %s", len(output.Items), mustJSON(t, result.StructuredContent))
	}
	return output.Items[0]
}
