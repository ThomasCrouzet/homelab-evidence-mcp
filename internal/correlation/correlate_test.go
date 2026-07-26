package correlation

import (
	"strings"
	"testing"
	"time"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/evidence"
)

func TestBuildTimeline_OrderAndNoRootCause(t *testing.T) {
	t0 := time.Date(2026, 7, 25, 2, 12, 0, 0, time.UTC)
	t1 := time.Date(2026, 7, 25, 2, 13, 0, 0, time.UTC)
	t2 := time.Date(2026, 7, 25, 2, 14, 0, 0, time.UTC)
	items := []evidence.Item{
		{ID: "c", Source: evidence.SourceGatus, ObservedAt: t2, Severity: evidence.SeverityError, Summary: "gatus fail"},
		{ID: "a", Source: evidence.SourceLoki, ObservedAt: t0, Severity: evidence.SeverityError, Summary: "timeout"},
		{ID: "b", Source: evidence.SourceDocker, ObservedAt: t1, Severity: evidence.SeverityWarning, Summary: "restarting"},
	}
	sources := []evidence.SourceOutcome{
		{SourceName: "gatus", Kind: evidence.SourceGatus, Status: "ok", ItemCount: 1},
		{SourceName: "docker", Kind: evidence.SourceDocker, Status: "ok", ItemCount: 1},
		{SourceName: "loki", Kind: evidence.SourceLoki, Status: "ok", ItemCount: 1},
		{SourceName: "hc", Kind: evidence.SourceHealthchecks, Status: "error", Error: "timeout"},
	}
	b := BuildTimeline("jellyfin", t0.Add(-time.Minute), t2.Add(time.Minute), items, sources, 100)
	if len(b.Items) != 3 {
		t.Fatal(len(b.Items))
	}
	if !b.Items[0].ObservedAt.Equal(t0) || !b.Items[2].ObservedAt.Equal(t2) {
		t.Fatalf("order %+v", b.Items)
	}
	if strings.Contains(b.FactualSummary, "root cause is") {
		t.Fatal(b.FactualSummary)
	}
	if !strings.Contains(b.FactualSummary, "does not establish root cause") {
		t.Fatal(b.FactualSummary)
	}
	if len(b.Warnings) == 0 {
		t.Fatal("expected source failure warning")
	}
}

func TestBuildTimeline_Truncation(t *testing.T) {
	var items []evidence.Item
	base := time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		items = append(items, evidence.Item{ID: string(rune('a' + i)), ObservedAt: base.Add(time.Duration(i) * time.Minute), Source: evidence.SourceLoki})
	}
	b := BuildTimeline("s", base, base.Add(time.Hour), items, nil, 3)
	if !b.Truncated || len(b.Items) != 3 {
		t.Fatalf("%v %d", b.Truncated, len(b.Items))
	}
}

func TestBuildTimeline_SourceTruncation(t *testing.T) {
	base := time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC)
	sources := []evidence.SourceOutcome{{
		SourceName: "loki",
		Kind:       evidence.SourceLoki,
		Status:     "ok",
		Truncated:  true,
	}}
	b := BuildTimeline("s", base, base.Add(time.Hour), nil, sources, 10)
	if !b.Truncated {
		t.Fatal("source truncation must surface on the bundle")
	}
	if !strings.Contains(strings.Join(b.Warnings, "\n"), "source loki (loki): truncated") {
		t.Fatalf("warnings=%v", b.Warnings)
	}
	if strings.Contains(strings.Join(b.Warnings, "\n"), "timeline truncated") {
		t.Fatalf("misleading timeline warning: %v", b.Warnings)
	}
}

func TestBuildTimeline_EmptyHonest(t *testing.T) {
	b := BuildTimeline("s", time.Now().Add(-time.Hour), time.Now(), nil, nil, 10)
	if !strings.Contains(b.FactualSummary, "Absence of evidence") {
		t.Fatal(b.FactualSummary)
	}
}

func TestBuildTimeline_FactualSummaryGoldenPhrases(t *testing.T) {
	now := time.Now().UTC()
	items := []evidence.Item{{
		ID: "1", Source: evidence.SourceGatus, SourceID: "e", Kind: evidence.KindEndpointCheck,
		ObservedAt: now, RetrievedAt: now, Summary: "fail", Severity: evidence.SeverityError,
	}}
	b := BuildTimeline("media", now.Add(-time.Hour), now, items, []evidence.SourceOutcome{{Status: "ok"}}, 10)
	for _, w := range []string{"Timeline is ordered by observed_at", "correlation does not establish root cause"} {
		if !strings.Contains(b.FactualSummary, w) {
			t.Fatalf("missing %q in %s", w, b.FactualSummary)
		}
	}
	low := strings.ToLower(b.FactualSummary)
	for _, f := range []string{"root cause is", "caused by", "therefore the failure"} {
		if strings.Contains(low, f) {
			t.Fatalf("forbidden phrase %q in %s", f, b.FactualSummary)
		}
	}
}
