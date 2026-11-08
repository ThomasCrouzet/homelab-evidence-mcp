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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// This test detects an evidence cache that exceeds its serialized byte budget.
func TestStdioEvidenceCacheByteBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	base := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	values := make([][]string, 0, 10)
	for index := 0; index < 10; index++ {
		values = append(values, []string{
			strconv.FormatInt(base.Add(time.Duration(index)*time.Second).UnixNano(), 10),
			fmt.Sprintf("line-%02d %s", index, strings.Repeat("x", 600)),
		})
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/loki/api/v1/query_range" {
			http.NotFound(w, r)
			return
		}
		streamValues := values
		container := "fixture"
		if strings.Contains(r.URL.Query().Get("query"), `container="huge"`) {
			container = "huge"
			streamValues = [][]string{{
				strconv.FormatInt(base.UnixNano(), 10),
				"huge " + strings.Repeat("y", 5000),
			}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"resultType": "streams",
				"result": []map[string]any{{
					"stream": map[string]string{"container": container},
					"values": streamValues,
				}},
			},
		})
	}))
	defer upstream.Close()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configText := fmt.Sprintf(`version: 1
limits:
  default_window: 15m
  max_log_lines: 20
  max_log_line_bytes: 16384
  max_evidence_items: 20
  evidence_cache_ttl: 5m
  evidence_cache_max: 100
  evidence_cache_max_bytes: 4096
  source_cache_ttl: 0s
sources:
  loki:
    kind: loki
    base_url: %s
services:
  - id: logs
    sources:
      loki: { source: loki, selector: '{container="fixture"}' }
  - id: huge
    sources:
      loki: { source: loki, selector: '{container="huge"}' }
`, upstream.URL)
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}

	binary := stdioTestBinary(t, ctx)
	var stderr bytes.Buffer
	command := exec.CommandContext(ctx, binary, "--config", configPath)
	command.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "cache-budget-fixture", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command, TerminateDuration: stdioShutdownTimeout}, nil)
	if err != nil {
		t.Fatalf("connect to executable: %v\nstderr:\n%s", err, stderr.String())
	}
	defer func() { _ = session.Close() }()

	logsResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "search_logs", Arguments: map[string]any{"service_id": "logs", "duration": "1h", "limit": 10},
	})
	if err != nil || logsResult.IsError {
		t.Fatalf("search_logs failed: %v", err)
	}
	var logsBody struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	executableE2EDecode(t, mustJSON(t, logsResult.StructuredContent), &logsBody)
	if len(logsBody.Items) != 10 {
		t.Fatalf("items=%d", len(logsBody.Items))
	}

	capabilities, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "evidence_capabilities"})
	if err != nil || capabilities.IsError {
		t.Fatalf("capabilities failed: %v", err)
	}
	var capsBody struct {
		Limits struct {
			EvidenceCacheMaxBytes int64 `json:"evidence_cache_max_bytes"`
		} `json:"limits"`
		EvidenceCache struct {
			Items     int    `json:"items"`
			Bytes     int64  `json:"bytes"`
			MaxBytes  int64  `json:"max_bytes"`
			Evictions uint64 `json:"evictions"`
			Oversized uint64 `json:"oversized_rejected"`
		} `json:"evidence_cache"`
	}
	capsRaw := mustJSON(t, capabilities.StructuredContent)
	executableE2EDecode(t, capsRaw, &capsBody)
	if capsBody.Limits.EvidenceCacheMaxBytes != 4096 || capsBody.EvidenceCache.MaxBytes != 4096 {
		t.Fatalf("configured byte budget is missing: %s", capsRaw)
	}
	if capsBody.EvidenceCache.Items >= 10 || capsBody.EvidenceCache.Bytes > 4096 ||
		capsBody.EvidenceCache.Evictions == 0 {
		t.Fatalf("cache exceeds its byte budget: %s", capsRaw)
	}
	for _, forbidden := range []string{"line-00", "yyyyy"} {
		if bytes.Contains(capsRaw, []byte(forbidden)) {
			t.Fatalf("capability counters contain evidence text: %s", capsRaw)
		}
	}

	oldest := logsBody.Items[0].ID
	missing, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "get_evidence", Arguments: map[string]any{"id": oldest},
	})
	if err != nil || missing == nil || !missing.IsError {
		t.Fatalf("evicted evidence is still retrievable: %v", err)
	}
	newest := logsBody.Items[len(logsBody.Items)-1].ID
	kept, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "get_evidence", Arguments: map[string]any{"id": newest},
	})
	if err != nil || kept.IsError {
		t.Fatalf("newest evidence was evicted: %v", err)
	}

	hugeResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "search_logs", Arguments: map[string]any{"service_id": "huge", "duration": "1h"},
	})
	if err != nil || hugeResult.IsError {
		t.Fatalf("huge search_logs failed: %v", err)
	}
	var hugeBody struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	executableE2EDecode(t, mustJSON(t, hugeResult.StructuredContent), &hugeBody)
	if len(hugeBody.Items) != 1 {
		t.Fatalf("huge items=%d", len(hugeBody.Items))
	}
	hugeCached, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "get_evidence", Arguments: map[string]any{"id": hugeBody.Items[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if hugeCached == nil || !hugeCached.IsError {
		t.Fatalf("an oversized item bypassed the byte budget: %s", mustJSON(t, hugeCached))
	}
	capabilities, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "evidence_capabilities"})
	if err != nil || capabilities.IsError {
		t.Fatalf("capabilities failed: %v", err)
	}
	executableE2EDecode(t, mustJSON(t, capabilities.StructuredContent), &capsBody)
	if capsBody.EvidenceCache.Oversized == 0 {
		t.Fatalf("oversized rejection is not reported: %s", mustJSON(t, capabilities.StructuredContent))
	}
}
