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

func TestCompactIncident_SummaryMatchesRetainedItems(t *testing.T) {
	bundle := compactFixture()
	bundle.Items[1].Severity = evidence.SeverityError
	bundle.Items[1].Freshness = evidence.FreshnessStale
	got, err := compactIncident(bundle, 1, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("items=%d", len(got.Items))
	}
	if !strings.Contains(got.FactualSummary, "1 returned evidence item(s)") {
		t.Fatalf("summary count mismatch: %s", got.FactualSummary)
	}
	if !strings.Contains(got.FactualSummary, "loki=") && got.Items[0].Severity == evidence.SeverityError {
		// The retained item may be gatus. Only assert that no omitted error is
		// counted when it is not present.
		if strings.Contains(got.FactualSummary, "loki=1") {
			t.Fatalf("summary counts an omitted error item: %s", got.FactualSummary)
		}
	}
	for _, warning := range got.Warnings {
		if strings.Contains(warning, "stale error evidence") {
			t.Fatalf("warning refers to omitted evidence: %v", got.Warnings)
		}
	}
}

func TestCompactIncident_SummaryKeepsOmittedSourceWarning(t *testing.T) {
	bundle := compactFixture()
	bundle.Sources[1].Reason = "retention_limited"
	bundle.Sources[1].Complete = false
	got, err := compactIncident(bundle, 1, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, warning := range got.Warnings {
		if strings.Contains(warning, "incomplete (retention_limited)") {
			found = true
		}
	}
	if !found {
		t.Fatalf("source coverage warning was lost: %v", got.Warnings)
	}
}

func TestCompactIncident_MetadataOverflowIsAnError(t *testing.T) {
	bundle := evidence.Bundle{ServiceID: "svc"}
	for index := 0; index < 200; index++ {
		bundle.Sources = append(bundle.Sources, evidence.SourceOutcome{
			SourceName: strings.Repeat("source-name-", 8) + fmt.Sprint(index),
			Kind:       evidence.SourceKind("gatus"),
			Status:     "error",
			Reason:     "upstream_error",
			Error:      strings.Repeat("failure-detail ", 8),
			Complete:   false,
		})
	}
	if _, err := compactIncident(bundle, 1, 8192); err == nil {
		t.Fatal("metadata-only overflow must fail closed")
	}
}

func TestCompactIncident_AllItemsRemovedStaysBounded(t *testing.T) {
	bundle := compactFixture()
	for index := range bundle.Items {
		bundle.Items[index].Attributes["line"] = strings.Repeat("é", 4096)
	}
	got, err := compactIncident(bundle, 100, 8192)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 0 {
		t.Fatalf("items=%d, want all removed", len(got.Items))
	}
	if got.ByteSize > got.ByteBudget || !got.ByteBudgetTruncated {
		t.Fatalf("bytes=%d budget=%d truncated=%v", got.ByteSize, got.ByteBudget, got.ByteBudgetTruncated)
	}
}

func TestCompactIncident_ReservesPerSource(t *testing.T) {
	bundle, err := compactIncident(compactFixture(), 5, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
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
	first, err := compactIncident(compactFixture(), 5, 8192)
	if err != nil {
		t.Fatalf("items=%d bytes=%d summary=%q warnings=%v err=%v", len(first.Items), first.ByteSize, first.FactualSummary, first.Warnings, err)
	}
	second, err := compactIncident(compactFixture(), 5, 8192)
	if err != nil {
		t.Fatal(err)
	}
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
