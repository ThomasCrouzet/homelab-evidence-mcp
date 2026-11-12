package mcpserver

import (
	"bytes"
	"context"
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

// This test detects a Gatus status snapshot that reports complete coverage
// while the selected endpoint contains records with an unusable timestamp or
// no success field. Fixtures are synthetic. The selected evidence must stay
// the latest valid record, and row order must not change the selection or the
// coverage.
func TestStdioGatusSnapshotCoverage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	now := time.Now().UTC().Truncate(time.Second)
	validSuccess := now.Add(-40 * time.Minute).Format(time.RFC3339)
	latestSuccess := now.Add(-50 * time.Minute).Format(time.RFC3339)
	olderValid := now.Add(-60 * time.Minute).Format(time.RFC3339)
	latestFailure := now.Add(-30 * time.Minute).Format(time.RFC3339)

	body := fmt.Sprintf(`[
		{"name":"mixed","key":"mixed","results":[
			{"success":true,"status":200,"timestamp":%q},
			{"success":false,"status":503,"timestamp":"malformed"}
		]},
		{"name":"mixed-reversed","key":"mixed-reversed","results":[
			{"success":false,"status":503,"timestamp":"malformed"},
			{"success":true,"status":200,"timestamp":%q}
		]},
		{"name":"missing-success","key":"missing-success","results":[
			{"success":true,"status":200,"timestamp":%q},
			{"status":500,"timestamp":%q}
		]},
		{"name":"valid","key":"valid","results":[
			{"success":true,"status":200,"timestamp":%q},
			{"success":false,"status":503,"timestamp":%q}
		]},
		{"name":"undated","key":"undated","results":[
			{"success":true,"status":200,"timestamp":"invalid"},
			{"success":false,"status":503,"timestamp":"also-invalid"}
		]},
		{"name":"empty","key":"empty","results":[]}
	]`, validSuccess, validSuccess, latestSuccess, olderValid, olderValid, latestFailure)

	gatus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/endpoints/statuses" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer gatus.Close()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configText := fmt.Sprintf(`version: 1
limits:
  default_window: 15m
  max_incident_window: 1h
sources:
  gatus:
    kind: gatus
    base_url: %s
services:
  - id: mixed
    sources:
      gatus: { source: gatus, endpoint_key: mixed }
  - id: mixed-reversed
    sources:
      gatus: { source: gatus, endpoint_key: mixed-reversed }
  - id: missing-success
    sources:
      gatus: { source: gatus, endpoint_key: missing-success }
  - id: valid
    sources:
      gatus: { source: gatus, endpoint_key: valid }
  - id: undated
    sources:
      gatus: { source: gatus, endpoint_key: undated }
  - id: empty
    sources:
      gatus: { source: gatus, endpoint_key: empty }
`, gatus.URL)
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}

	binary := stdioTestBinary(t, ctx)
	var stderr bytes.Buffer
	command := exec.CommandContext(ctx, binary, "--config", configPath)
	command.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "gatus-snapshot-coverage", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command, TerminateDuration: stdioShutdownTimeout}, nil)
	if err != nil {
		t.Fatalf("connect to executable: %v\nstderr:\n%s", err, stderr.String())
	}
	defer func() { _ = session.Close() }()

	type snapshotItem struct {
		ObservedAt time.Time      `json:"observed_at"`
		Freshness  string         `json:"freshness"`
		Severity   string         `json:"severity"`
		Attributes map[string]any `json:"attributes"`
	}
	type snapshotSource struct {
		Kind      string `json:"kind"`
		Status    string `json:"status"`
		Reason    string `json:"reason"`
		Complete  bool   `json:"complete"`
		ItemCount int    `json:"item_count"`
		Discarded int    `json:"discarded"`
	}
	type snapshotBody struct {
		Items          []snapshotItem   `json:"items"`
		Sources        []snapshotSource `json:"sources"`
		Complete       bool             `json:"complete"`
		SourceComplete bool             `json:"source_complete"`
	}
	call := func(service string) snapshotBody {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name: "service_status", Arguments: map[string]any{"service_id": service},
		})
		if err != nil || result.IsError {
			t.Fatalf("%s service_status failed: %v\n%s", service, err, stderr.String())
		}
		var out snapshotBody
		executableE2EDecode(t, mustJSON(t, result.StructuredContent), &out)
		return out
	}
	gatusSource := func(service string, out snapshotBody) snapshotSource {
		t.Helper()
		for _, source := range out.Sources {
			if source.Kind == "gatus" {
				return source
			}
		}
		t.Fatalf("%s has no gatus source: %+v", service, out.Sources)
		return snapshotSource{}
	}
	oneItem := func(service string, out snapshotBody) snapshotItem {
		t.Helper()
		if len(out.Items) != 1 {
			t.Fatalf("%s items=%d: %s", service, len(out.Items), mustJSON(t, out))
		}
		return out.Items[0]
	}

	mixed := call("mixed")
	mixedItem := oneItem("mixed", mixed)
	if mixedItem.Severity != "info" ||
		mixedItem.Attributes["success"] != true ||
		mixedItem.Attributes["http_status"] != float64(200) ||
		mixedItem.Attributes["source_time_known"] != true {
		t.Fatalf("valid latest status was not retained: %+v", mixedItem)
	}
	if got := gatusSource("mixed", mixed); got.Status != "ok" || got.Reason != "partial_invalid_records" ||
		got.Complete || got.Discarded != 1 {
		t.Fatalf("mixed coverage=%+v", got)
	}
	if mixed.SourceComplete || mixed.Complete {
		t.Fatalf("mixed snapshot claimed complete coverage: %s", mustJSON(t, mixed))
	}

	reversed := call("mixed-reversed")
	reversedItem := oneItem("mixed-reversed", reversed)
	if got := gatusSource("mixed-reversed", reversed); got.Reason != "partial_invalid_records" ||
		got.Complete || got.Discarded != 1 {
		t.Fatalf("reversed coverage=%+v", got)
	}
	if mixedItem.Attributes["success"] != reversedItem.Attributes["success"] ||
		mixedItem.Attributes["success_known"] != reversedItem.Attributes["success_known"] ||
		mixedItem.Attributes["http_status"] != reversedItem.Attributes["http_status"] ||
		mixedItem.Attributes["source_time_known"] != reversedItem.Attributes["source_time_known"] ||
		mixedItem.Severity != reversedItem.Severity ||
		mixedItem.Freshness != reversedItem.Freshness ||
		!mixedItem.ObservedAt.Equal(reversedItem.ObservedAt) {
		t.Fatalf("row order changed the selected evidence:\nforward=%+v\nreverse=%+v", mixedItem, reversedItem)
	}

	missingSuccessBody := call("missing-success")
	missingItem := oneItem("missing-success", missingSuccessBody)
	if missingItem.Attributes["http_status"] != float64(200) ||
		missingItem.Attributes["success_known"] != true ||
		missingItem.Severity != "info" {
		t.Fatalf("a missing-success sibling changed the selected record: %+v", missingItem)
	}
	if got := gatusSource("missing-success", missingSuccessBody); got.Reason != "partial_invalid_records" ||
		got.Complete || got.Discarded != 1 {
		t.Fatalf("missing-success coverage=%+v", got)
	}

	valid := call("valid")
	validItem := oneItem("valid", valid)
	if validItem.Severity != "error" || validItem.Attributes["http_status"] != float64(503) {
		t.Fatalf("fully valid snapshot selected the wrong record: %+v", validItem)
	}
	if got := gatusSource("valid", valid); got.Reason != "" || !got.Complete || got.Discarded != 0 {
		t.Fatalf("fully valid snapshot lost complete coverage: %+v", got)
	}
	if !valid.SourceComplete || !valid.Complete {
		t.Fatalf("fully valid snapshot is not complete: %s", mustJSON(t, valid))
	}

	undated := call("undated")
	undatedItem := oneItem("undated", undated)
	if undatedItem.Attributes["source_time_known"] != false ||
		undatedItem.Attributes["collection_time_derived"] != true ||
		undatedItem.Freshness != "unknown" {
		t.Fatalf("wholly undated snapshot looked dated: %+v", undatedItem)
	}
	if got := gatusSource("undated", undated); got.Reason != "partial_invalid_records" ||
		got.Complete || got.Discarded != 2 {
		t.Fatalf("wholly undated coverage=%+v", got)
	}

	empty := call("empty")
	emptyItem := oneItem("empty", empty)
	if emptyItem.Severity != "unknown" || emptyItem.Attributes["results"] != float64(0) {
		t.Fatalf("empty endpoint gained a state: %+v", emptyItem)
	}
	if got := gatusSource("empty", empty); got.Reason != "empty_result" || !got.Complete || got.Discarded != 0 {
		t.Fatalf("empty endpoint coverage=%+v", got)
	}
}
