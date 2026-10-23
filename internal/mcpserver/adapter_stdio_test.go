package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestStdioAdapterCorrections(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	base := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)

	gatusServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"key": "another-endpoint", "name": "another-endpoint", "results": []any{},
		}})
	}))
	defer gatusServer.Close()

	lokiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entries := [][]string{
			{strconv.FormatInt(base.UnixNano(), 10), "line-1"},
			{strconv.FormatInt(base.Add(time.Second).UnixNano(), 10), "line-2"},
			{strconv.FormatInt(base.Add(2*time.Second).UnixNano(), 10), "line-3"},
		}
		if r.URL.Query().Get("direction") == "backward" {
			sort.Slice(entries, func(i, j int) bool { return entries[i][0] > entries[j][0] })
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit > 0 && len(entries) > limit {
			entries = entries[:limit]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"resultType": "streams",
				"result": []map[string]any{{
					"stream": map[string]string{"app": "fixture"},
					"values": entries,
				}},
			},
		})
	}))
	defer lokiServer.Close()

	ntfyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"id":"notice","time":%d,"event":"message","message":"fixture"}`+"\n", base.Unix())
	}))
	defer ntfyServer.Close()

	beszelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		if page == "" || page == "1" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"page": 1, "perPage": 1, "totalItems": 2, "totalPages": 2,
				"items": []map[string]any{{"name": "first-host", "status": "up"}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"page": 2, "perPage": 1, "totalItems": 2, "totalPages": 2,
			"items": []map[string]any{{
				"status": "up",
				"info":   map[string]any{"h": "page-two-host", "cpu": 0, "mp": 0},
			}},
		})
	}))
	defer beszelServer.Close()

	incompleteBeszelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page <= 0 {
			page = 1
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"page": page, "perPage": 1, "totalItems": 10000, "totalPages": 10000,
			"items": []map[string]any{{"name": fmt.Sprintf("host-%d", page), "status": "up"}},
		})
	}))
	defer incompleteBeszelServer.Close()

	emptyBeszelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"page": 1, "perPage": 200, "totalItems": 0, "totalPages": 0,
			"items": []map[string]any{},
		})
	}))
	defer emptyBeszelServer.Close()

	oversizedBeszelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		items := make([]map[string]any, 6401)
		for index := range items {
			items[index] = map[string]any{"name": fmt.Sprintf("oversized-host-%d", index), "status": "up"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"page": 1, "perPage": 200, "totalItems": len(items), "totalPages": 1,
			"items": items,
		})
	}))
	defer oversizedBeszelServer.Close()

	shortBeszelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page <= 0 {
			page = 1
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"page": page, "perPage": 2, "totalItems": 3, "totalPages": 2,
			"items": []map[string]any{{"name": fmt.Sprintf("short-host-%d", page), "status": "up"}},
		})
	}))
	defer shortBeszelServer.Close()

	binary := stdioTestBinary(t, ctx)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configText := fmt.Sprintf(`version: 1
limits:
  max_incident_window: 168h
  max_log_lines: 10
  max_evidence_items: 100
  per_source_timeout: 5s
  total_timeout: 20s
sources:
  gatus:
    kind: gatus
    base_url: %s
  loki:
    kind: loki
    base_url: %s
  ntfy:
    kind: ntfy
    base_url: %s
  beszel:
    kind: beszel
    base_url: %s
  beszel-incomplete:
    kind: beszel
    base_url: %s
  beszel-empty:
    kind: beszel
    base_url: %s
  beszel-oversized:
    kind: beszel
    base_url: %s
  beszel-short:
    kind: beszel
    base_url: %s
services:
  - id: fixture
    display_name: Fixture
    sources:
      gatus: { source: gatus, endpoint_key: missing-endpoint }
      loki: { source: loki, selector: '{app="fixture"}' }
      ntfy: { source: ntfy, topic: secret-topic }
      beszel: { source: beszel, system_name: page-two-host }
  - id: incomplete
    display_name: Incomplete
    sources:
      beszel: { source: beszel-incomplete, system_name: absent-host }
  - id: empty
    display_name: Empty
    sources:
      beszel: { source: beszel-empty, system_name: absent-host }
  - id: oversized
    display_name: Oversized
    sources:
      beszel: { source: beszel-oversized, system_name: absent-host }
  - id: short
    display_name: Short
    sources:
      beszel: { source: beszel-short, system_name: absent-host }
redact:
  - exact: secret-topic
`, gatusServer.URL, lokiServer.URL, ntfyServer.URL, beszelServer.URL, incompleteBeszelServer.URL, emptyBeszelServer.URL, oversizedBeszelServer.URL, shortBeszelServer.URL)
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}

	command := exec.CommandContext(ctx, binary, "--config", configPath)
	client := mcp.NewClient(&mcp.Implementation{Name: "adapter-fixture", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command, TerminateDuration: stdioShutdownTimeout}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	search := adapterCallTool(t, ctx, session, "search_logs", map[string]any{
		"service_id": "fixture",
		"start":      base.Add(-time.Second).Format(time.RFC3339Nano),
		"end":        base.Add(3 * time.Second).Format(time.RFC3339Nano),
		"limit":      2,
	})
	var logOutput struct {
		Items []struct {
			Attributes map[string]any `json:"attributes"`
		} `json:"items"`
		Source struct {
			Truncated bool `json:"truncated"`
		} `json:"source"`
	}
	adapterDecodeContent(t, search, &logOutput)
	if len(logOutput.Items) != 2 || logOutput.Items[0].Attributes["line"] != "line-2" || logOutput.Items[1].Attributes["line"] != "line-3" {
		t.Fatalf("Loki did not return the newest bounded lines: %+v", logOutput.Items)
	}
	if !logOutput.Source.Truncated {
		t.Fatal("Loki did not report upstream result truncation")
	}

	recent := adapterCallTool(t, ctx, session, "incident_context", map[string]any{
		"service_id": "fixture",
		"start":      base.Add(-time.Second).Format(time.RFC3339Nano),
		"end":        base.Add(3 * time.Second).Format(time.RFC3339Nano),
	})
	var recentOutput struct {
		Items []struct {
			Source            string         `json:"source"`
			Attributes        map[string]any `json:"attributes"`
			RedactionsApplied int            `json:"redactions_applied"`
		} `json:"items"`
	}
	adapterDecodeContent(t, recent, &recentOutput)
	foundNtfy := false
	for _, item := range recentOutput.Items {
		if item.Source != "ntfy" {
			continue
		}
		foundNtfy = true
		topic, _ := item.Attributes["topic"].(string)
		if strings.Contains(topic, "secret-topic") || !strings.Contains(topic, "[REDACTED]") || item.RedactionsApplied == 0 {
			t.Fatalf("ntfy topic bypassed redaction: %+v", item)
		}
	}
	if !foundNtfy {
		t.Fatal("ntfy fixture item is missing")
	}

	historical := adapterCallTool(t, ctx, session, "incident_context", map[string]any{
		"service_id": "fixture",
		"start":      base.Add(-24 * time.Hour).Format(time.RFC3339Nano),
		"end":        base.Add(-23 * time.Hour).Format(time.RFC3339Nano),
	})
	var historicalOutput struct {
		Items []struct {
			Source string `json:"source"`
		} `json:"items"`
	}
	adapterDecodeContent(t, historical, &historicalOutput)
	for _, item := range historicalOutput.Items {
		if item.Source == "gatus" {
			t.Fatal("Gatus put a current missing endpoint in a historical window")
		}
	}

	status := adapterCallTool(t, ctx, session, "service_status", map[string]any{"service_id": "fixture"})
	var statusOutput struct {
		Items []struct {
			Source     string         `json:"source"`
			Attributes map[string]any `json:"attributes"`
		} `json:"items"`
	}
	adapterDecodeContent(t, status, &statusOutput)
	foundBeszel := false
	for _, item := range statusOutput.Items {
		if item.Source == "beszel" && item.Attributes["system_name"] == "page-two-host" && item.Attributes["found"] == true &&
			item.Attributes["cpu"] == float64(0) && item.Attributes["mem"] == float64(0) {
			foundBeszel = true
		}
	}
	if !foundBeszel {
		t.Fatal("Beszel did not decode current zero metrics on page two")
	}

	incomplete := adapterCallTool(t, ctx, session, "service_status", map[string]any{"service_id": "incomplete"})
	var incompleteOutput struct {
		Items []struct {
			Source     string         `json:"source"`
			Attributes map[string]any `json:"attributes"`
		} `json:"items"`
		Sources []struct {
			Kind   string `json:"kind"`
			Status string `json:"status"`
		} `json:"sources"`
	}
	adapterDecodeContent(t, incomplete, &incompleteOutput)
	for _, item := range incompleteOutput.Items {
		if item.Source == "beszel" && item.Attributes["found"] == false {
			t.Fatal("Beszel reported a missing system from an incomplete collection")
		}
	}
	foundIncompleteError := false
	for _, source := range incompleteOutput.Sources {
		if source.Kind == "beszel" && source.Status == "error" {
			foundIncompleteError = true
		}
	}
	if !foundIncompleteError {
		t.Fatal("Beszel did not report an incomplete paginated collection")
	}

	empty := adapterCallTool(t, ctx, session, "service_status", map[string]any{"service_id": "empty"})
	var emptyOutput struct {
		Items []struct {
			Source     string         `json:"source"`
			Attributes map[string]any `json:"attributes"`
		} `json:"items"`
		Sources []struct {
			Kind   string `json:"kind"`
			Status string `json:"status"`
		} `json:"sources"`
	}
	adapterDecodeContent(t, empty, &emptyOutput)
	foundEmptyMissing := false
	for _, item := range emptyOutput.Items {
		if item.Source == "beszel" && item.Attributes["found"] == false {
			foundEmptyMissing = true
		}
	}
	foundEmptyOK := false
	for _, source := range emptyOutput.Sources {
		if source.Kind == "beszel" && source.Status == "ok" {
			foundEmptyOK = true
		}
	}
	if !foundEmptyMissing || !foundEmptyOK {
		t.Errorf("Beszel rejected a valid empty PocketBase collection: %+v", emptyOutput)
	}

	oversized := adapterCallTool(t, ctx, session, "service_status", map[string]any{"service_id": "oversized"})
	var oversizedOutput struct {
		Items []struct {
			Source     string         `json:"source"`
			Attributes map[string]any `json:"attributes"`
		} `json:"items"`
		Sources []struct {
			Kind   string `json:"kind"`
			Status string `json:"status"`
		} `json:"sources"`
	}
	adapterDecodeContent(t, oversized, &oversizedOutput)
	foundOversizedError := false
	for _, source := range oversizedOutput.Sources {
		if source.Kind == "beszel" && source.Status == "error" {
			foundOversizedError = true
		}
	}
	for _, item := range oversizedOutput.Items {
		if item.Source == "beszel" && item.Attributes["found"] == false {
			t.Error("Beszel reported a missing system from an oversized collection")
		}
	}
	if !foundOversizedError {
		t.Error("Beszel did not reject a collection above the record limit")
	}

	short := adapterCallTool(t, ctx, session, "service_status", map[string]any{"service_id": "short"})
	var shortOutput struct {
		Items []struct {
			Source     string         `json:"source"`
			Attributes map[string]any `json:"attributes"`
		} `json:"items"`
		Sources []struct {
			Kind   string `json:"kind"`
			Status string `json:"status"`
		} `json:"sources"`
	}
	adapterDecodeContent(t, short, &shortOutput)
	foundShortError := false
	for _, source := range shortOutput.Sources {
		if source.Kind == "beszel" && source.Status == "error" {
			foundShortError = true
		}
	}
	for _, item := range shortOutput.Items {
		if item.Source == "beszel" && item.Attributes["found"] == false {
			t.Error("Beszel reported a missing system from a short collection")
		}
	}
	if !foundShortError {
		t.Error("Beszel accepted fewer records than totalItems")
	}
}

func adapterCallTool(t *testing.T, ctx context.Context, session *mcp.ClientSession, name string, arguments map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil || result.IsError {
		t.Fatalf("%s failed: %v", name, err)
	}
	return result
}

func adapterDecodeContent(t *testing.T, result *mcp.CallToolResult, target any) {
	t.Helper()
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, target); err != nil {
		t.Fatal(err)
	}
}
