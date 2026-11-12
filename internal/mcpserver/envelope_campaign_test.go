package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/evidence"
)

// envelopeWireSize applies the documented accounting rule to a returned value:
// the pretty-printed text block, the compact structured content, and the
// documented 512-byte MCP envelope allowance. byte_size describes the emitted
// payloads, so the measured value includes the byte_size field itself.
func envelopeWireSize(t *testing.T, value any) int {
	t.Helper()
	text, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("marshal text payload: %v", err)
	}
	structured, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal structured payload: %v", err)
	}
	return len(text) + len(structured) + 512
}

// smallestItemWireSize gives the smallest cost that one more retained item
// adds to both payloads.
func smallestItemWireSize(t *testing.T, items []evidence.Item) int {
	t.Helper()
	smallest := -1
	for _, item := range items {
		text, err := json.MarshalIndent(item, "", "  ")
		if err != nil {
			t.Fatalf("marshal item text: %v", err)
		}
		structured, err := json.Marshal(item)
		if err != nil {
			t.Fatalf("marshal item: %v", err)
		}
		cost := len(text) + len(structured) + 1
		if smallest < 0 || cost < smallest {
			smallest = cost
		}
	}
	if smallest < 0 {
		t.Fatal("no retained item to measure")
	}
	return smallest
}

func errorBodyText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	body := toolErrorBody(t, res)
	message, _ := body["error"].(string)
	return message
}

// TestByteSizeMatchesFinalEnvelope detects a byte_size that measured the
// payload before its own digits were written. Every evidence tool must report
// the size of the final text and structured payloads.
func TestByteSizeMatchesFinalEnvelope(t *testing.T) {
	logsApp := budgetApp(t, lokiBulkServer(t).URL)

	_, out, err := logsApp.toolSearchLogs(context.Background(), nil, searchLogsIn{ServiceID: "logs", Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	logs := out.(SearchLogsOut)
	if got := envelopeWireSize(t, logs); got != logs.ByteSize {
		t.Fatalf("search_logs byte_size=%d, final envelope=%d", logs.ByteSize, got)
	}
	if logs.ByteSize > logs.ByteBudget {
		t.Fatalf("search_logs byte_size=%d exceeds budget=%d", logs.ByteSize, logs.ByteBudget)
	}
	if smallestItemWireSize(t, logs.Items) <= logs.ByteBudget-logs.ByteSize {
		t.Fatalf("search_logs stopped too early: gap=%d next item=%d",
			logs.ByteBudget-logs.ByteSize, smallestItemWireSize(t, logs.Items))
	}

	_, out, err = logsApp.toolIncidentContext(context.Background(), nil, incidentIn{ServiceID: "logs"})
	if err != nil {
		t.Fatal(err)
	}
	incident := out.(IncidentContextOut)
	if got := envelopeWireSize(t, incident); got != incident.ByteSize {
		t.Fatalf("incident_context byte_size=%d, final envelope=%d", incident.ByteSize, got)
	}
	if incident.ByteSize > incident.ByteBudget {
		t.Fatalf("incident_context byte_size=%d exceeds budget=%d", incident.ByteSize, incident.ByteBudget)
	}

	_, out, err = logsApp.toolIncidentContext(context.Background(), nil, incidentIn{ServiceID: "logs", Compact: true})
	if err != nil {
		t.Fatal(err)
	}
	compact := out.(IncidentContextOut)
	if got := envelopeWireSize(t, compact); got != compact.ByteSize {
		t.Fatalf("compact incident_context byte_size=%d, final envelope=%d", compact.ByteSize, got)
	}
	if compact.ByteSize > compact.ByteBudget {
		t.Fatalf("compact incident_context byte_size=%d exceeds budget=%d", compact.ByteSize, compact.ByteBudget)
	}

	_, out, err = logsApp.toolGetEvidence(context.Background(), nil, getEvidenceIn{ID: logs.Items[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	evidenceOut := out.(GetEvidenceOut)
	if got := envelopeWireSize(t, evidenceOut); got != evidenceOut.ByteSize {
		t.Fatalf("get_evidence byte_size=%d, final envelope=%d", evidenceOut.ByteSize, got)
	}
	if evidenceOut.ByteSize > evidenceOut.ByteBudget {
		t.Fatalf("get_evidence byte_size=%d exceeds budget=%d", evidenceOut.ByteSize, evidenceOut.ByteBudget)
	}

	cronsApp := healthchecksBulkApp(t)
	_, out, err = cronsApp.toolServiceStatus(context.Background(), nil, serviceIDIn{ServiceID: "crons"})
	if err != nil {
		t.Fatal(err)
	}
	status := out.(ServiceStatusOut)
	if got := envelopeWireSize(t, status); got != status.ByteSize {
		t.Fatalf("service_status byte_size=%d, final envelope=%d", status.ByteSize, got)
	}
	if status.ByteSize > status.ByteBudget {
		t.Fatalf("service_status byte_size=%d exceeds budget=%d", status.ByteSize, status.ByteBudget)
	}

	_, out, err = cronsApp.toolFailedCrons(context.Background(), nil, failedCronsIn{Duration: "1h"})
	if err != nil {
		t.Fatal(err)
	}
	crons := out.(FailedCronsOut)
	if got := envelopeWireSize(t, crons); got != crons.ByteSize {
		t.Fatalf("failed_crons byte_size=%d, final envelope=%d", crons.ByteSize, got)
	}
	if crons.ByteSize > crons.ByteBudget {
		t.Fatalf("failed_crons byte_size=%d exceeds budget=%d", crons.ByteSize, crons.ByteBudget)
	}
}

// TestByteSizeFinalEnvelopeBoundary detects a budget decision made on a size
// that differs from the emitted envelope. Adapter timestamps carry sub-second
// precision, so the comparison uses a margin far below one evidence item.
func TestByteSizeFinalEnvelopeBoundary(t *testing.T) {
	const margin = 64

	t.Run("get_evidence", func(t *testing.T) {
		app := budgetApp(t, lokiBulkServer(t).URL)
		app.Cache.Put(evidence.Item{
			ID: "boundary", Source: evidence.SourceLoki, SourceID: "boundary",
			ObservedAt: time.Now().UTC().Add(-time.Hour), RetrievedAt: time.Now().UTC(),
			Summary: "boundary item",
			Attributes: map[string]any{
				"line": "level=error " + strings.Repeat("x", 900),
			},
		})
		app.Cfg.Limits.MaxIncidentBytes = 1 << 20
		_, out, err := app.toolGetEvidence(context.Background(), nil, getEvidenceIn{ID: "boundary"})
		if err != nil {
			t.Fatal(err)
		}
		fitted := out.(GetEvidenceOut)
		measured := envelopeWireSize(t, fitted)
		if measured != fitted.ByteSize {
			t.Fatalf("byte_size=%d, final envelope=%d", fitted.ByteSize, measured)
		}

		app.Cfg.Limits.MaxIncidentBytes = measured + margin
		res, out, err := app.toolGetEvidence(context.Background(), nil, getEvidenceIn{ID: "boundary"})
		if err != nil {
			t.Fatal(err)
		}
		if res.IsError {
			t.Fatalf("budget %d rejected an item that fits: %s", measured+margin, errorBodyText(t, res))
		}
		accepted := out.(GetEvidenceOut)
		if accepted.ByteSize != envelopeWireSize(t, accepted) || accepted.ByteSize > measured+margin {
			t.Fatalf("accepted byte_size=%d, envelope=%d, budget=%d",
				accepted.ByteSize, envelopeWireSize(t, accepted), measured+margin)
		}

		app.Cfg.Limits.MaxIncidentBytes = measured - margin
		res, _, err = app.toolGetEvidence(context.Background(), nil, getEvidenceIn{ID: "boundary"})
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError {
			t.Fatalf("budget %d below the final envelope accepted the item", measured-margin)
		}
	})

	t.Run("failed_crons", func(t *testing.T) {
		app := healthchecksBulkApp(t)
		app.Cfg.Limits.MaxIncidentBytes = 1 << 20
		_, out, err := app.toolFailedCrons(context.Background(), nil, failedCronsIn{Duration: "1h"})
		if err != nil {
			t.Fatal(err)
		}
		full := out.(FailedCronsOut)
		if len(full.Items) == 0 {
			t.Fatal("fixture returned no failed check to trim")
		}
		if got := envelopeWireSize(t, full); got != full.ByteSize {
			t.Fatalf("byte_size=%d, final envelope=%d", full.ByteSize, got)
		}
		delta := boundaryDelta(len(full.Items))
		budget := full.ByteSize - delta
		if envelopeWireSize(t, full) <= budget {
			t.Fatalf("the untrimmed envelope %d fits the boundary %d", full.ByteSize, budget)
		}

		app.Cfg.Limits.MaxIncidentBytes = budget
		res, out, err := app.toolFailedCrons(context.Background(), nil, failedCronsIn{Duration: "1h"})
		if err != nil {
			t.Fatal(err)
		}
		if res.IsError {
			t.Fatalf("budget %d must trim, not fail: %s", budget, errorBodyText(t, res))
		}
		trimmed := out.(FailedCronsOut)
		if len(trimmed.Items) == 0 {
			t.Fatal("source returned no item at the boundary budget")
		}
		actual := envelopeWireSize(t, trimmed)
		if actual != trimmed.ByteSize {
			t.Fatalf("byte_size=%d, final envelope=%d", trimmed.ByteSize, actual)
		}
		if actual > budget {
			t.Fatalf("byte_size=%d exceeds budget=%d", actual, budget)
		}
		if !trimmed.ByteBudgetTruncated || trimmed.OmittedItems <= full.OmittedItems {
			t.Fatalf("the boundary did not omit one more item: truncated=%v omitted=%d before=%d",
				trimmed.ByteBudgetTruncated, trimmed.OmittedItems, full.OmittedItems)
		}
	})
}

// TestByteSizeMetadataOnlyError keeps the explicit failure when source
// metadata alone cannot fit the response budget.
func TestByteSizeMetadataOnlyError(t *testing.T) {
	app := healthchecksBulkApp(t)
	app.Cfg.Limits.MaxIncidentBytes = 600
	res, _, err := app.toolFailedCrons(context.Background(), nil, failedCronsIn{Duration: "1h"})
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("metadata larger than the budget must fail: %+v", res)
	}
}
