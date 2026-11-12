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

// This test detects a Healthchecks source request budget that a warm source
// cache hit, a coalesced wait, or a canceled caller spends. It runs the real
// executable and counts fixture GET requests.
func TestStdioHealthchecksBudgetCountsUpstreamRequests(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	binary := stdioTestBinary(t, ctx)

	const uniqueKey = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	t.Run("warm cache hits do not spend the source budget", func(t *testing.T) {
		subCtx, subCancel := context.WithTimeout(ctx, time.Minute)
		defer subCancel()
		var listCalls atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/api/v3/checks/" {
				http.NotFound(w, r)
				return
			}
			listCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"checks": []map[string]any{{
				"name": "backup", "slug": "backup", "status": "up", "tags": "cron",
				"unique_key": uniqueKey,
			}}})
		}))
		defer upstream.Close()

		session, stderr := integration190Session(t, subCtx, binary, fmt.Sprintf(`version: 1
limits:
  source_cache_ttl: 1m
  max_source_requests_per_minute: 1
  per_source_timeout: 2s
  total_timeout: 10s
sources:
  hc:
    kind: healthchecks
    base_url: %s
    token_env: HEM_HC_TOKEN
services:
  - id: quota
    sources:
      healthchecks: { source: hc, check_unique_key: %s }
`, upstream.URL, uniqueKey))

		first := integration190StatusRead(t, subCtx, session, stderr)
		second := integration190StatusRead(t, subCtx, session, stderr)
		if len(first.Items) != 1 || len(second.Items) != 1 {
			t.Fatalf("items first=%d second=%d, want 1 each", len(first.Items), len(second.Items))
		}
		if !first.Items[0].ObservedAt.Equal(second.Items[0].ObservedAt) {
			t.Fatalf("second read did not reuse the cached snapshot: %s then %s",
				first.Items[0].ObservedAt, second.Items[0].ObservedAt)
		}
		if got := listCalls.Load(); got != 1 {
			t.Fatalf("upstream requests=%d, want 1: a warm cache hit sent a request", got)
		}
	})

	t.Run("cache-disabled cold reads still stop at the budget", func(t *testing.T) {
		subCtx, subCancel := context.WithTimeout(ctx, time.Minute)
		defer subCancel()
		var listCalls atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/api/v3/checks/" {
				http.NotFound(w, r)
				return
			}
			listCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"checks": []map[string]any{{
				"name": "backup", "slug": "backup", "status": "up", "tags": "cron",
				"unique_key": uniqueKey,
			}}})
		}))
		defer upstream.Close()

		session, stderr := integration190Session(t, subCtx, binary, fmt.Sprintf(`version: 1
limits:
  source_cache_ttl: 0
  max_source_requests_per_minute: 1
  per_source_timeout: 2s
  total_timeout: 10s
sources:
  hc:
    kind: healthchecks
    base_url: %s
    token_env: HEM_HC_TOKEN
services:
  - id: quota
    sources:
      healthchecks: { source: hc, check_unique_key: %s }
`, upstream.URL, uniqueKey))

		first := integration190StatusRead(t, subCtx, session, stderr)
		if len(first.Items) != 1 {
			t.Fatalf("first cold read items=%d, want 1", len(first.Items))
		}
		second := integration190StatusRead(t, subCtx, session, stderr)
		if len(second.Items) != 0 {
			t.Fatalf("second cold read items=%d, want 0: the budget must fail closed", len(second.Items))
		}
		found := false
		for _, source := range second.Sources {
			if source.Kind != "healthchecks" {
				continue
			}
			found = true
			if source.Status != "error" || source.ItemCount != 0 {
				t.Fatalf("second read source status=%q item_count=%d, want error and 0", source.Status, source.ItemCount)
			}
			if !strings.Contains(strings.ToLower(source.Error), "request budget") {
				t.Fatalf("second read error=%q, want the request budget diagnostic", source.Error)
			}
		}
		if !found {
			t.Fatalf("second read has no healthchecks source outcome: %s", mustJSON(t, second))
		}
		if got := listCalls.Load(); got != 1 {
			t.Fatalf("upstream requests=%d, want 1: a rejected cold read sent a request", got)
		}
	})

	t.Run("warm transitions reuse list and flips without spending quota", func(t *testing.T) {
		subCtx, subCancel := context.WithTimeout(ctx, time.Minute)
		defer subCancel()
		var listCalls, flipCalls atomic.Int32
		now := time.Now().UTC().Truncate(time.Second)
		flipAt := now.Add(-10 * time.Minute).Format(time.RFC3339)
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				http.NotFound(w, r)
				return
			}
			if r.URL.Path == "/api/v3/checks/" {
				listCalls.Add(1)
				_ = json.NewEncoder(w).Encode(map[string]any{"checks": []map[string]any{{
					"name": "backup", "slug": "backup", "status": "up", "tags": "cron",
					"unique_key": uniqueKey,
				}}})
				return
			}
			if strings.HasSuffix(r.URL.Path, "/flips/") {
				flipCalls.Add(1)
				_ = json.NewEncoder(w).Encode(map[string]any{"flips": []map[string]any{{
					"timestamp": flipAt, "up": 0,
				}}})
				return
			}
			http.NotFound(w, r)
		}))
		defer upstream.Close()

		session, stderr := integration190Session(t, subCtx, binary, fmt.Sprintf(`version: 1
limits:
  source_cache_ttl: 1m
  max_source_requests_per_minute: 2
  max_cron_window: 24h
  per_source_timeout: 2s
  total_timeout: 10s
sources:
  hc:
    kind: healthchecks
    base_url: %s
    token_env: HEM_HC_TOKEN
services:
  - id: history
    sources:
      healthchecks:
        source: hc
        check_unique_key: %s
        history_mode: transitions
`, upstream.URL, uniqueKey))

		start := now.Add(-20 * time.Minute).Format(time.RFC3339)
		end := now.Add(-time.Minute).Format(time.RFC3339)
		for attempt := 1; attempt <= 2; attempt++ {
			out := integration190FailedCronsRead(t, subCtx, session, stderr, start, end)
			if len(out.Items) != 1 {
				t.Fatalf("failed_crons %d items=%d, want 1", attempt, len(out.Items))
			}
		}
		if got := listCalls.Load(); got != 1 {
			t.Fatalf("list requests=%d, want 1", got)
		}
		if got := flipCalls.Load(); got != 1 {
			t.Fatalf("flips requests=%d, want 1", got)
		}
	})
}

type integration190Status struct {
	Items []struct {
		ObservedAt time.Time `json:"observed_at"`
	} `json:"items"`
	Sources []struct {
		Kind      string `json:"kind"`
		Status    string `json:"status"`
		Reason    string `json:"reason"`
		Error     string `json:"error"`
		ItemCount int    `json:"item_count"`
	} `json:"sources"`
}

func integration190Session(t *testing.T, ctx context.Context, binary, configText string) (*mcp.ClientSession, *bytes.Buffer) {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command := exec.CommandContext(ctx, binary, "--config", configPath)
	command.Env = append(os.Environ(), "HEM_HC_TOKEN=fixture-quota-token")
	command.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "integration-190", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command, TerminateDuration: stdioShutdownTimeout}, nil)
	if err != nil {
		t.Fatalf("connect to executable: %v\nstderr:\n%s", err, stderr.String())
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, &stderr
}

func integration190StatusRead(t *testing.T, ctx context.Context, session *mcp.ClientSession, stderr *bytes.Buffer) integration190Status {
	t.Helper()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "service_status", Arguments: map[string]any{"service_id": "quota"},
	})
	if err != nil {
		t.Fatalf("service_status: %v\nstderr:\n%s", err, stderr.String())
	}
	if result.IsError {
		t.Fatalf("service_status returned an error: %s", mustJSON(t, result))
	}
	var out integration190Status
	executableE2EDecode(t, mustJSON(t, result.StructuredContent), &out)
	return out
}

func integration190FailedCronsRead(t *testing.T, ctx context.Context, session *mcp.ClientSession, stderr *bytes.Buffer, start, end string) integration190Status {
	t.Helper()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "failed_crons",
		Arguments: map[string]any{
			"service_id": "history", "start": start, "end": end,
		},
	})
	if err != nil {
		t.Fatalf("failed_crons: %v\nstderr:\n%s", err, stderr.String())
	}
	if result.IsError {
		t.Fatalf("failed_crons returned an error: %s", mustJSON(t, result))
	}
	var out integration190Status
	executableE2EDecode(t, mustJSON(t, result.StructuredContent), &out)
	return out
}
