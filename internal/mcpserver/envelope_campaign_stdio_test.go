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
	"sync"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type envelopeCampaignFixture struct {
	ctx         context.Context
	session     *mcp.ClientSession
	schemas     map[string]*jsonschema.Resolved
	stderr      *bytes.Buffer
	slowStarted chan struct{}
}

// envelopeCampaignOptions select the tool budgets and fixture latency.
type envelopeCampaignOptions struct {
	maxCallsPerMinute  int
	maxConcurrentTools int
}

type envelopeCampaignBody struct {
	ByteSize            int      `json:"byte_size"`
	ByteBudget          int      `json:"byte_budget"`
	ByteBudgetTruncated bool     `json:"byte_budget_truncated"`
	OmittedItems        int      `json:"omitted_items"`
	Complete            bool     `json:"complete"`
	SourceComplete      bool     `json:"source_complete"`
	DeliveryComplete    bool     `json:"delivery_complete"`
	Truncated           bool     `json:"truncated"`
	TimeBasis           string   `json:"time_basis"`
	Warnings            []string `json:"warnings"`
	Items               []struct {
		ID         string         `json:"id"`
		Source     string         `json:"source"`
		Attributes map[string]any `json:"attributes"`
	} `json:"items"`
}

// This executable test detects a byte_size that does not match the emitted
// text and structured payloads of every evidence tool.
func TestStdioEnvelopeByteAccounting(t *testing.T) {
	fixture := startEnvelopeCampaignFixture(t)
	calls := []struct {
		name string
		args map[string]any
	}{
		{name: "service_status", args: map[string]any{"service_id": "campaign"}},
		{name: "incident_context", args: map[string]any{"service_id": "campaign"}},
		{name: "incident_context", args: map[string]any{"service_id": "campaign", "compact": true}},
		{name: "search_logs", args: map[string]any{"service_id": "campaign"}},
		{name: "failed_crons", args: map[string]any{
			"start": time.Now().UTC().Add(-23*time.Hour - 50*time.Minute).Format(time.RFC3339),
			"end":   time.Now().UTC().Add(10 * time.Minute).Format(time.RFC3339),
		}},
	}
	seen := map[string]bool{}
	for _, call := range calls {
		result, body := fixture.callOK(t, call.name, call.args)
		key := call.name
		if compact, _ := call.args["compact"].(bool); compact {
			key = "compact incident_context"
		}
		seen[key] = true
		if body.ByteSize <= 0 || body.ByteBudget <= 0 {
			t.Fatalf("%s byte accounting is empty: size=%d budget=%d", key, body.ByteSize, body.ByteBudget)
		}
		if got := envelopeWireSizeOfResult(t, result); got != body.ByteSize {
			t.Fatalf("%s byte_size=%d, final envelope=%d", key, body.ByteSize, got)
		}
		if body.ByteSize > body.ByteBudget {
			t.Fatalf("%s byte_size=%d exceeds budget=%d", key, body.ByteSize, body.ByteBudget)
		}
		if key != "compact incident_context" && !body.ByteBudgetTruncated {
			t.Fatalf("%s fixture did not exercise byte trimming", key)
		}
		if strings.Contains(key, "incident_context") {
			assertWarningByteSizes(t, key, body)
		}
	}
	for _, want := range []string{"service_status", "incident_context", "compact incident_context", "search_logs", "failed_crons"} {
		if !seen[want] {
			t.Fatalf("%s was not checked", want)
		}
	}

	// A cached item is measured with the same final envelope rule.
	_, logs := fixture.callOK(t, "search_logs", map[string]any{"service_id": "campaign"})
	if len(logs.Items) == 0 {
		t.Fatal("search_logs returned no item id for get_evidence")
	}
	result, body := fixture.callOK(t, "get_evidence", map[string]any{"id": logs.Items[0].ID})
	if got := envelopeWireSizeOfResult(t, result); got != body.ByteSize {
		t.Fatalf("get_evidence byte_size=%d, final envelope=%d", body.ByteSize, got)
	}
	if body.ByteSize > body.ByteBudget {
		t.Fatalf("get_evidence byte_size=%d exceeds budget=%d", body.ByteSize, body.ByteBudget)
	}
}

// This executable test detects a search_logs complete alias that disagrees
// with source_complete after a local byte-budget trim.
func TestStdioSearchLogsCoverageFlags(t *testing.T) {
	fixture := startEnvelopeCampaignFixture(t)
	result, body := fixture.callOK(t, "search_logs", map[string]any{"service_id": "campaign"})
	if !body.ByteBudgetTruncated || body.OmittedItems == 0 {
		t.Fatalf("expected a local delivery trim: truncated=%v omitted=%d",
			body.ByteBudgetTruncated, body.OmittedItems)
	}
	if !body.Complete || !body.SourceComplete {
		t.Fatalf("complete=%v source_complete=%v after a local trim of a complete source",
			body.Complete, body.SourceComplete)
	}
	if body.DeliveryComplete {
		t.Fatal("trimmed search_logs claims complete delivery")
	}
	if body.ByteSize > body.ByteBudget {
		t.Fatalf("search_logs byte_size=%d exceeds budget=%d", body.ByteSize, body.ByteBudget)
	}
	if got := envelopeWireSizeOfResult(t, result); got != body.ByteSize {
		t.Fatalf("search_logs byte_size=%d, final envelope=%d", body.ByteSize, got)
	}

	_, cut := fixture.callOK(t, "search_logs", map[string]any{"service_id": "campaign", "limit": 3})
	if cut.Complete || cut.SourceComplete {
		t.Fatalf("truncated upstream source reports complete=%v source_complete=%v",
			cut.Complete, cut.SourceComplete)
	}
	if cut.DeliveryComplete {
		t.Fatal("truncated upstream source reports complete delivery")
	}
}

// This executable test detects a time_basis that describes discarded
// evidence instead of the retained items.
func TestStdioFailedCronsTimeBasis(t *testing.T) {
	fixture := startEnvelopeCampaignFixture(t)
	start := time.Now().UTC().Add(-23*time.Hour - 50*time.Minute).Format(time.RFC3339)
	end := time.Now().UTC().Add(10 * time.Minute).Format(time.RFC3339)
	result, body := fixture.callOK(t, "failed_crons", map[string]any{"start": start, "end": end})
	if !body.ByteBudgetTruncated || body.OmittedItems == 0 {
		t.Fatalf("expected a byte trim: truncated=%v omitted=%d",
			body.ByteBudgetTruncated, body.OmittedItems)
	}
	current, historical := false, false
	for _, item := range body.Items {
		if item.Source != "healthchecks" {
			continue
		}
		if value, ok := item.Attributes["current"].(bool); ok {
			if value {
				current = true
			} else {
				historical = true
			}
		}
	}
	if current || !historical {
		t.Fatalf("retained categories current=%v historical=%v", current, historical)
	}
	if body.TimeBasis != "historical_transitions" {
		t.Fatalf("time_basis=%s after retaining only historical transitions", body.TimeBasis)
	}
	if got := envelopeWireSizeOfResult(t, result); got != body.ByteSize {
		t.Fatalf("failed_crons byte_size=%d, final envelope=%d", body.ByteSize, got)
	}
}

// This executable test detects tool errors without a stable documented reason.
func TestStdioToolErrorReasons(t *testing.T) {
	fixture := startEnvelopeCampaignFixture(t)
	fixture.callError(t, "list_services", map[string]any{"prefix": strings.Repeat("p", 129)}, "invalid_arguments")
	fixture.callError(t, "get_evidence", map[string]any{"id": ""}, "invalid_arguments")
	fixture.callError(t, "get_evidence", map[string]any{"id": "campaign-missing"}, "evidence_not_found")
	fixture.callError(t, "service_status", map[string]any{"service_id": "campaign-missing"}, "unknown_service")
	fixture.callError(t, "incident_context", map[string]any{"service_id": "campaign", "max_items": -1}, "invalid_arguments")
	fixture.callError(t, "search_logs", map[string]any{"service_id": "cron-history"}, "binding_missing")
}

// This executable test detects tool budget refusals without a stable reason.
func TestStdioToolBudgetReasons(t *testing.T) {
	t.Run("tool_rate_limit", func(t *testing.T) {
		fixture := startEnvelopeCampaignFixtureWith(t, envelopeCampaignOptions{maxCallsPerMinute: 3, maxConcurrentTools: 4})
		for index := 0; index < 3; index++ {
			fixture.callOK(t, "list_services", map[string]any{})
		}
		fixture.callError(t, "list_services", map[string]any{}, "tool_rate_limit")
	})

	t.Run("concurrent_tool_limit", func(t *testing.T) {
		fixture := startEnvelopeCampaignFixtureWith(t, envelopeCampaignOptions{maxCallsPerMinute: 100, maxConcurrentTools: 1})
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = fixture.session.CallTool(fixture.ctx, &mcp.CallToolParams{
				Name: "search_logs", Arguments: map[string]any{"service_id": "slow-logs"},
			})
		}()
		select {
		case <-fixture.slowStarted:
		case <-time.After(5 * time.Second):
			t.Fatal("the slow source request did not start")
		}
		fixture.callError(t, "list_services", map[string]any{}, "concurrent_tool_limit")
		<-done
	})
}

func startEnvelopeCampaignFixture(t *testing.T) *envelopeCampaignFixture {
	t.Helper()
	return startEnvelopeCampaignFixtureWith(t, envelopeCampaignOptions{maxCallsPerMinute: 1000, maxConcurrentTools: 8})
}

func startEnvelopeCampaignFixtureWith(t *testing.T, options envelopeCampaignOptions) *envelopeCampaignFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	now := time.Now().UTC().Truncate(time.Second)
	t0 := now.Add(-5 * time.Minute)

	gatus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/endpoints/statuses" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"name": "campaign endpoint", "group": "campaign", "key": "campaign",
			"results": []map[string]any{
				{"success": true, "status": 200, "timestamp": t0.Format(time.RFC3339)},
				{"success": false, "status": 503, "timestamp": t0.Add(time.Minute).Format(time.RFC3339)},
			},
		}})
	}))
	t.Cleanup(gatus.Close)

	docker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/containers/json" || r.URL.Query().Get("all") != "true" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"Names": []string{"/campaign-app"}, "Image": "example/campaign:1",
			"State": "restarting", "Status": "Restarting (1) 3 seconds ago",
		}})
	}))
	t.Cleanup(docker.Close)

	slowStarted := make(chan struct{})
	var slowOnce sync.Once
	loki := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/loki/api/v1/query_range" {
			http.NotFound(w, r)
			return
		}
		if strings.Contains(r.URL.Query().Get("query"), "slow") {
			slowOnce.Do(func() { close(slowStarted) })
			time.Sleep(900 * time.Millisecond)
		}
		values := make([][]string, 0, 120)
		for index := 0; index < 120; index++ {
			values = append(values, []string{
				fmt.Sprintf("%d", t0.Add(time.Duration(index)*time.Second).UnixNano()),
				fmt.Sprintf("line-%03d level=info %s", index, strings.Repeat("l", 400)),
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"resultType": "streams",
				"result": []map[string]any{{
					"stream": map[string]string{"container": "campaign"},
					"values": values,
				}},
			},
		})
	}))
	t.Cleanup(loki.Close)

	checks := []map[string]any{{
		"name": "nightly", "slug": "nightly", "status": "up",
		"unique_key": strings.Repeat("a", 32),
	}}
	for index := 0; index < 12; index++ {
		checks = append(checks, map[string]any{
			"name": fmt.Sprintf("watch-%02d", index), "slug": fmt.Sprintf("watch-%02d", index),
			"tags": "current", "status": "down", "unique_key": fmt.Sprintf("%032d", index),
		})
	}
	flips := make([]map[string]any, 0, 48)
	for index := 0; index < 40; index++ {
		at := now.Add(-23*time.Hour + time.Duration(index)*20*time.Minute)
		flips = append(flips, map[string]any{"timestamp": at.Format(time.RFC3339), "up": index % 2})
	}
	for index := 1; index <= 8; index++ {
		flips = append(flips, map[string]any{
			"timestamp": now.Add(time.Duration(index) * time.Minute).Format(time.RFC3339), "up": 0,
		})
	}
	healthchecks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/checks/":
			_ = json.NewEncoder(w).Encode(map[string]any{"checks": checks})
		case strings.HasPrefix(r.URL.Path, "/api/v3/checks/") && strings.HasSuffix(r.URL.Path, "/flips/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"flips": flips})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(healthchecks.Close)

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configText := fmt.Sprintf(`version: 1
limits:
  default_window: 15m
  max_incident_window: 24h
  max_cron_window: 24h
  max_log_lines: 200
  max_evidence_items: 200
  max_incident_response_bytes: 8192
  per_source_timeout: 2s
  total_timeout: 10s
  source_cache_ttl: 0s
  max_tool_calls_per_minute: %d
  max_concurrent_tools: %d
sources:
  gatus:
    kind: gatus
    base_url: %s
  docker:
    kind: docker
    base_url: %s
  loki:
    kind: loki
    base_url: %s
  hc:
    kind: healthchecks
    base_url: %s
    token_env: HEM_ENVELOPE_TOKEN
services:
  - id: campaign
    sources:
      gatus: { source: gatus, endpoint_key: campaign }
      docker: { source: docker, container_name: campaign-app }
      loki: { source: loki, selector: '{container="campaign"}' }
      healthchecks: { source: hc, check_tags: [current] }
  - id: cron-history
    sources:
      healthchecks: { source: hc, check_name: nightly, history_mode: transitions }
  - id: slow-logs
    sources:
      loki: { source: loki, selector: '{container="slow"}' }
`, options.maxCallsPerMinute, options.maxConcurrentTools, gatus.URL, docker.URL, loki.URL, healthchecks.URL)
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HEM_ENVELOPE_TOKEN", "fixture-token")

	binary := stdioTestBinary(t, ctx)
	var stderr bytes.Buffer
	command := exec.CommandContext(ctx, binary, "--config", configPath)
	command.Env = os.Environ()
	command.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "envelope-campaign", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command, TerminateDuration: stdioShutdownTimeout}, nil)
	if err != nil {
		t.Fatalf("connect to executable: %v\nstderr:\n%s", err, stderr.String())
	}
	t.Cleanup(func() { _ = session.Close() })

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	schemas := make(map[string]*jsonschema.Resolved, len(tools.Tools))
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
	}
	return &envelopeCampaignFixture{ctx: ctx, session: session, schemas: schemas, stderr: &stderr, slowStarted: slowStarted}
}

func (f *envelopeCampaignFixture) callOK(t *testing.T, name string, args map[string]any) (*mcp.CallToolResult, envelopeCampaignBody) {
	t.Helper()
	result, err := f.session.CallTool(f.ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v\nstderr:\n%s", name, err, f.stderr.String())
	}
	if result.IsError {
		t.Fatalf("%s returned an error: %s", name, mustJSON(t, result))
	}
	if result.StructuredContent == nil {
		t.Fatalf("%s returned no structured content", name)
	}
	validateWireResult(t, name, result, f.schemas[name])
	var body envelopeCampaignBody
	if err := json.Unmarshal(structuredBytes(t, result), &body); err != nil {
		t.Fatalf("%s decode: %v", name, err)
	}
	return result, body
}

func (f *envelopeCampaignFixture) callError(t *testing.T, name string, args map[string]any, wantReason string) {
	t.Helper()
	result, err := f.session.CallTool(f.ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if !result.IsError {
		t.Fatalf("%s did not return a tool error: %s", name, mustJSON(t, result))
	}
	if result.StructuredContent != nil {
		t.Fatalf("%s error carries structured content: %s", name, mustJSON(t, result))
	}
	if len(result.Content) != 1 {
		t.Fatalf("%s error content count=%d", name, len(result.Content))
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("%s error content type=%T", name, result.Content[0])
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(text.Text), &body); err != nil {
		t.Fatalf("%s error body is not JSON: %v\n%s", name, err, text.Text)
	}
	reason, _ := body["reason"].(string)
	if reason != wantReason {
		t.Fatalf("%s reason=%q, want %q: %s", name, reason, wantReason, text.Text)
	}
	if !containsString(documentedToolErrorReasons, reason) {
		t.Fatalf("%s reason %q is outside the documented set", name, reason)
	}
	message, _ := body["error"].(string)
	if strings.TrimSpace(message) == "" {
		t.Fatalf("%s error result has no readable message: %s", name, text.Text)
	}
}

// assertWarningByteSizes detects a warning that quotes a byte_size other than
// the final field. The warning text is part of the measured payload.
func assertWarningByteSizes(t *testing.T, label string, body envelopeCampaignBody) {
	t.Helper()
	if !body.ByteBudgetTruncated {
		t.Fatalf("%s fixture did not exercise byte trimming", label)
	}
	found := false
	for _, warning := range body.Warnings {
		if strings.Contains(warning, "byte budget omitted") {
			found = true
		}
	}
	if !found {
		t.Fatalf("%s: no byte budget warning: %v", label, body.Warnings)
	}
	for _, value := range warningByteSizes(t, body.Warnings) {
		if value != body.ByteSize {
			t.Fatalf("%s: warning byte_size=%d, final byte_size=%d: %v", label, value, body.ByteSize, body.Warnings)
		}
	}
}

func envelopeWireSizeOfResult(t *testing.T, result *mcp.CallToolResult) int {
	t.Helper()
	if len(result.Content) != 1 {
		t.Fatalf("content count=%d", len(result.Content))
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content type=%T", result.Content[0])
	}
	structured := structuredBytes(t, result)
	return len(text.Text) + len(structured) + 512
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
