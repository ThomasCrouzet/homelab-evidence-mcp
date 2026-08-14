package evidence

import (
	"testing"
	"time"
)

func TestCache_TTLAndEvict(t *testing.T) {
	c := NewCache(50*time.Millisecond, 2)
	c.Put(Item{ID: "a", Summary: "1"})
	c.Put(Item{ID: "b", Summary: "2"})
	c.Put(Item{ID: "c", Summary: "3"})
	if len(c.items) > 2 {
		t.Fatal(len(c.items))
	}
	// The item may have been evicted by the capacity limit.
	_, _ = c.Get("a")
	got, ok := c.Get("c")
	if !ok || got.Freshness != FreshnessCached {
		t.Fatalf("%v %v", ok, got)
	}
	time.Sleep(60 * time.Millisecond)
	if _, ok := c.Get("c"); ok {
		t.Fatal("should expire")
	}
}

func TestNewCache_ZeroDisables(t *testing.T) {
	c := NewCache(time.Minute, 0)
	c.Put(Item{ID: "a", Summary: "1"})
	if _, ok := c.Get("a"); ok {
		t.Fatal("max 0 must disable storage")
	}
}

func TestNewCache_EnforcesHardLimit(t *testing.T) {
	c := NewCache(time.Minute, maxCacheItems+1)
	if c.max != maxCacheItems {
		t.Fatalf("limit=%d", c.max)
	}
}

func TestSortOutcomes(t *testing.T) {
	o := []SourceOutcome{
		{Kind: SourceLoki, SourceName: "b"},
		{Kind: SourceDocker, SourceName: "a"},
		{Kind: SourceDocker, SourceName: "z"},
	}
	SortOutcomes(o)
	if o[0].Kind != SourceDocker || o[0].SourceName != "a" {
		t.Fatalf("%+v", o)
	}
	if o[1].SourceName != "z" || o[2].Kind != SourceLoki {
		t.Fatalf("%+v", o)
	}
}

func TestKeepNewest(t *testing.T) {
	t0 := time.Unix(100, 0).UTC()
	items := []Item{
		{ID: "old", ObservedAt: t0, Source: SourceLoki},
		{ID: "mid", ObservedAt: t0.Add(time.Second), Source: SourceLoki},
		{ID: "new", ObservedAt: t0.Add(2 * time.Second), Source: SourceLoki},
	}
	got, truncated := KeepNewest(items, 2)
	if !truncated || len(got) != 2 {
		t.Fatalf("got=%+v truncated=%v", got, truncated)
	}
	if got[0].ID != "mid" || got[1].ID != "new" {
		t.Fatalf("did not keep newest: %+v", got)
	}
}

func TestComputeFreshness(t *testing.T) {
	now := time.Unix(1_000_000, 0).UTC()
	if ComputeFreshness(time.Time{}, now) != FreshnessUnknown {
		t.Fatal("zero")
	}
	if ComputeFreshness(now.Add(-time.Minute), now) != FreshnessLive {
		t.Fatal("live")
	}
	if ComputeFreshness(now.Add(-10*time.Minute), now) != FreshnessRecent {
		t.Fatal("recent")
	}
	if ComputeFreshness(now.Add(-time.Hour), now) != FreshnessStale {
		t.Fatal("stale")
	}
}

func TestSortItems(t *testing.T) {
	t0 := time.Unix(100, 0).UTC()
	items := []Item{
		{ID: "b", Source: SourceDocker, ObservedAt: t0},
		{ID: "a", Source: SourceGatus, ObservedAt: t0},
		{ID: "c", Source: SourceGatus, ObservedAt: t0.Add(time.Second)},
	}
	SortItems(items)
	// On equal timestamps: source ascending then id.
	if items[0].ID != "b" || items[1].ID != "a" || items[2].ID != "c" {
		t.Fatalf("%+v", items)
	}
}
