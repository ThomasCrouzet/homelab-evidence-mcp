package mcpserver

import (
	"context"
	"testing"
)

// boundaryDelta exceeds the largest serialized difference that sub-second
// adapter timestamps can add between two calls. Adapters share one fetch time
// across the items of a source, and RFC3339Nano drops trailing fraction zeros,
// so the width can change by up to 20 bytes per item. The delta stays below the
// cost of one evidence item only when a probe needs that property; the probes
// below only require that it exceeds the jitter.
func boundaryDelta(items int) int {
	return 48*items + 512
}

// assertNearBoundary proves that a tool accepts the full retained set when the
// budget equals its final envelope plus a small margin, and that it omits at
// least one more item when the budget is below the final envelope. The probe
// fails the test when the reported byte_size differs from the emitted payloads.
func assertNearBoundary(t *testing.T, setBudget func(int), probe func() (size, omitted, items int, ok bool)) {
	t.Helper()
	setBudget(1 << 20)
	full, fullOmitted, fullItems, ok := probe()
	if !ok {
		t.Fatal("the full response failed")
	}
	if fullItems == 0 {
		t.Fatal("the probe retrieved no item")
	}
	delta := boundaryDelta(fullItems)

	setBudget(full + delta)
	accepted, acceptedOmitted, _, ok := probe()
	if !ok {
		t.Fatalf("budget %d rejected a response that fits", full+delta)
	}
	if accepted > full+delta {
		t.Fatalf("accepted byte_size=%d exceeds budget=%d", accepted, full+delta)
	}
	if acceptedOmitted != fullOmitted {
		t.Fatalf("a larger budget omitted items: omitted=%d, full=%d", acceptedOmitted, fullOmitted)
	}

	setBudget(full - delta)
	trimmed, trimmedOmitted, _, ok := probe()
	if !ok {
		t.Fatalf("budget %d failed instead of trimming", full-delta)
	}
	if trimmed > full-delta {
		t.Fatalf("trimmed byte_size=%d exceeds budget=%d", trimmed, full-delta)
	}
	if trimmedOmitted <= acceptedOmitted {
		t.Fatalf("the smaller budget omitted no item: omitted=%d, accepted=%d", trimmedOmitted, acceptedOmitted)
	}
}

// TestByteSizeNearBoundaryPerTool adds the accept and reject cases that the
// byte accounting criterion names for every evidence tool with a budget.
func TestByteSizeNearBoundaryPerTool(t *testing.T) {
	t.Run("service_status", func(t *testing.T) {
		app := healthchecksBulkApp(t)
		assertNearBoundary(t,
			func(budget int) { app.Cfg.Limits.MaxIncidentBytes = budget },
			func() (int, int, int, bool) {
				res, out, err := app.toolServiceStatus(context.Background(), nil, serviceIDIn{ServiceID: "crons"})
				if err != nil {
					t.Fatal(err)
				}
				if res.IsError {
					return 0, 0, 0, false
				}
				status := out.(ServiceStatusOut)
				if got := envelopeWireSize(t, status); got != status.ByteSize {
					t.Fatalf("byte_size=%d, final envelope=%d", status.ByteSize, got)
				}
				return status.ByteSize, status.OmittedItems, len(status.Items), true
			})
	})

	t.Run("incident_context", func(t *testing.T) {
		app := budgetApp(t, lokiBulkServer(t).URL)
		assertNearBoundary(t,
			func(budget int) { app.Cfg.Limits.MaxIncidentBytes = budget },
			func() (int, int, int, bool) {
				res, out, err := app.toolIncidentContext(context.Background(), nil, incidentIn{ServiceID: "logs"})
				if err != nil {
					t.Fatal(err)
				}
				if res.IsError {
					return 0, 0, 0, false
				}
				incident := out.(IncidentContextOut)
				if got := envelopeWireSize(t, incident); got != incident.ByteSize {
					t.Fatalf("byte_size=%d, final envelope=%d", incident.ByteSize, got)
				}
				return incident.ByteSize, incident.OmittedItems, len(incident.Items), true
			})
	})

	t.Run("incident_context_compact", func(t *testing.T) {
		app := budgetApp(t, lokiBulkServer(t).URL)
		assertNearBoundary(t,
			func(budget int) { app.Cfg.Limits.MaxIncidentBytes = budget },
			func() (int, int, int, bool) {
				res, out, err := app.toolIncidentContext(context.Background(), nil, incidentIn{ServiceID: "logs", Compact: true})
				if err != nil {
					t.Fatal(err)
				}
				if res.IsError {
					return 0, 0, 0, false
				}
				incident := out.(IncidentContextOut)
				if got := envelopeWireSize(t, incident); got != incident.ByteSize {
					t.Fatalf("byte_size=%d, final envelope=%d", incident.ByteSize, got)
				}
				return incident.ByteSize, incident.OmittedItems, len(incident.Items), true
			})
	})

	t.Run("search_logs", func(t *testing.T) {
		app := budgetApp(t, lokiBulkServer(t).URL)
		assertNearBoundary(t,
			func(budget int) { app.Cfg.Limits.MaxIncidentBytes = budget },
			func() (int, int, int, bool) {
				res, out, err := app.toolSearchLogs(context.Background(), nil, searchLogsIn{ServiceID: "logs", Limit: 200})
				if err != nil {
					t.Fatal(err)
				}
				if res.IsError {
					return 0, 0, 0, false
				}
				logs := out.(SearchLogsOut)
				if got := envelopeWireSize(t, logs); got != logs.ByteSize {
					t.Fatalf("byte_size=%d, final envelope=%d", logs.ByteSize, got)
				}
				return logs.ByteSize, logs.OmittedItems, len(logs.Items), true
			})
	})
}
