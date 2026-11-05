package mcpserver

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/evidence"
)

func compactFixture() evidence.Bundle {
	t0 := time.Unix(1_800_000_000, 0).UTC()
	items := []evidence.Item{{
		ID: "gatus-1", Source: evidence.SourceGatus, SourceID: "api",
		ObservedAt: t0, Summary: "gatus success", Severity: evidence.SeverityInfo,
		Attributes: map[string]any{"line": strings.Repeat("g", 200)},
	}}
	for index := 0; index < 20; index++ {
		items = append(items, evidence.Item{
			ID: fmt.Sprintf("loki-%02d", index), Source: evidence.SourceLoki,
			SourceID: "container=api", ObservedAt: t0.Add(time.Duration(index+1) * time.Second),
			Summary: "loki line", Severity: evidence.SeverityInfo,
			Attributes: map[string]any{"line": strings.Repeat("é", 300)},
		})
	}
	return evidence.Bundle{
		ServiceID: "svc",
		Items:     items,
		Sources: []evidence.SourceOutcome{
			{Kind: evidence.SourceGatus, Status: "ok", ItemCount: 1, Complete: true},
			{Kind: evidence.SourceLoki, Status: "ok", ItemCount: 20, Complete: true},
		},
		Complete: true,
	}
}

func TestCompactIncident_ReservesPerSource(t *testing.T) {
	bundle := compactIncident(compactFixture(), 5, 1<<20)
	if !bundle.Compact || bundle.ByteBudget != 1<<20 {
		t.Fatalf("flags=%+v", bundle)
	}
	if len(bundle.Items) != 5 || !bundle.SelectionTruncated || bundle.OmittedItems != 16 {
		t.Fatalf("selection items=%d truncated=%v omitted=%d", len(bundle.Items), bundle.SelectionTruncated, bundle.OmittedItems)
	}
	sources := map[evidence.SourceKind]int{}
	for _, item := range bundle.Items {
		sources[item.Source]++
	}
	if sources[evidence.SourceGatus] != 1 || sources[evidence.SourceLoki] != 4 {
		t.Fatalf("reservation sources=%+v", sources)
	}
	if bundle.ByteSize > bundle.ByteBudget {
		t.Fatalf("byte size=%d budget=%d", bundle.ByteSize, bundle.ByteBudget)
	}
}

func TestCompactIncident_ByteBudgetAndDeterminism(t *testing.T) {
	first := compactIncident(compactFixture(), 5, 8192)
	second := compactIncident(compactFixture(), 5, 8192)
	if first.ByteSize > first.ByteBudget {
		t.Fatalf("byte size=%d budget=%d", first.ByteSize, first.ByteBudget)
	}
	if len(first.Items) != len(second.Items) {
		t.Fatalf("nondeterministic item count: %d and %d", len(first.Items), len(second.Items))
	}
	for index := range first.Items {
		if first.Items[index].ID != second.Items[index].ID {
			t.Fatalf("nondeterministic item at %d: %s and %s", index, first.Items[index].ID, second.Items[index].ID)
		}
	}
	if strings.Contains(fmt.Sprint(first.Items), "\ufffd") {
		t.Fatal("compact selection broke UTF-8")
	}
}
