package mcpserver

import (
	"context"
	"testing"
)

// TestSearchLogsCoverageFlagsAfterTrim detects a local delivery trim that
// changes the legacy complete alias while the source stayed complete.
func TestSearchLogsCoverageFlagsAfterTrim(t *testing.T) {
	app := budgetApp(t, lokiBulkServer(t).URL)
	_, out, err := app.toolSearchLogs(context.Background(), nil, searchLogsIn{ServiceID: "logs", Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	trimmed := out.(SearchLogsOut)
	if !trimmed.ByteBudgetTruncated || trimmed.OmittedItems == 0 || trimmed.DeliveryComplete {
		t.Fatalf("expected a delivery trim: truncated=%v omitted=%d delivery=%v",
			trimmed.ByteBudgetTruncated, trimmed.OmittedItems, trimmed.DeliveryComplete)
	}
	if !trimmed.Complete || !trimmed.SourceComplete {
		t.Fatalf("complete=%v source_complete=%v after a local trim of a complete source",
			trimmed.Complete, trimmed.SourceComplete)
	}

	_, out, err = app.toolSearchLogs(context.Background(), nil, searchLogsIn{ServiceID: "logs", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	cut := out.(SearchLogsOut)
	if cut.Complete || cut.SourceComplete {
		t.Fatalf("truncated upstream source reports complete=%v source_complete=%v",
			cut.Complete, cut.SourceComplete)
	}
}
