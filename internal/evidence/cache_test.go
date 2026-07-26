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
