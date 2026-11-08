package evidence

import (
	"strings"
	"testing"
	"time"
)

func TestCache_DefensiveCopies(t *testing.T) {
	c := NewCache(time.Minute, 10, DefaultCacheMaxBytes)
	tags := []string{"original"}
	item := Item{
		ID: "ev", Summary: "s", Freshness: FreshnessUnknown,
		Attributes: map[string]any{
			"tags":   tags,
			"nested": map[string]any{"key": "value"},
			"count":  2,
		},
	}
	c.Put(item)
	// Mutating the caller's values must not change retained evidence.
	tags[0] = "mutated"
	item.Attributes["extra"] = "added"
	item.Attributes["nested"].(map[string]any)["key"] = "changed"
	statsBefore := c.Stats()

	got, ok := c.Get("ev")
	if !ok {
		t.Fatal("item missing")
	}
	if got.Attributes["tags"].([]string)[0] != "original" {
		t.Fatalf("stored tags mutated: %+v", got.Attributes)
	}
	if got.Attributes["nested"].(map[string]any)["key"] != "value" {
		t.Fatalf("stored nested map mutated: %+v", got.Attributes)
	}
	if _, exists := got.Attributes["extra"]; exists {
		t.Fatalf("stored map gained a key: %+v", got.Attributes)
	}
	if c.Stats().Bytes != statsBefore.Bytes {
		t.Fatalf("byte accounting changed: %d -> %d", statsBefore.Bytes, c.Stats().Bytes)
	}

	// Mutating a returned copy must not change the next read either.
	got.Attributes["tags"].([]string)[0] = "returned-mutation"
	got.Attributes["nested"].(map[string]any)["key"] = "returned-change"
	again, _ := c.Get("ev")
	if again.Attributes["tags"].([]string)[0] != "original" ||
		again.Attributes["nested"].(map[string]any)["key"] != "value" {
		t.Fatalf("returned copy aliased the cache: %+v", again.Attributes)
	}
	if again.Freshness != FreshnessCached || again.SourceFreshness != FreshnessUnknown || again.CacheReadAt == nil {
		t.Fatalf("cache delivery state=%+v", again)
	}
}

func TestCache_GetKeepsSourceFreshness(t *testing.T) {
	c := NewCache(time.Minute, 10, DefaultCacheMaxBytes)
	c.Put(Item{ID: "stale", Freshness: FreshnessStale})
	c.Put(Item{ID: "live", Freshness: FreshnessLive})
	stale, _ := c.Get("stale")
	live, _ := c.Get("live")
	if stale.SourceFreshness != FreshnessStale || stale.Freshness != FreshnessCached {
		t.Fatalf("stale=%+v", stale)
	}
	if live.SourceFreshness != FreshnessLive || live.Freshness != FreshnessCached {
		t.Fatalf("live=%+v", live)
	}
}

func TestCache_TTLAndEvict(t *testing.T) {
	c := NewCache(50*time.Millisecond, 2, DefaultCacheMaxBytes)
	c.Put(Item{ID: "a", Summary: "1"})
	c.Put(Item{ID: "b", Summary: "2"})
	c.Put(Item{ID: "c", Summary: "3"})
	if len(c.items) > 2 {
		t.Fatal(len(c.items))
	}
	// The capacity limit can remove the item.
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
	c := NewCache(time.Minute, 0, DefaultCacheMaxBytes)
	c.Put(Item{ID: "a", Summary: "1"})
	if _, ok := c.Get("a"); ok {
		t.Fatal("max 0 must deactivate storage")
	}
}

func TestNewCache_EnforcesHardLimit(t *testing.T) {
	c := NewCache(time.Minute, maxCacheItems+1, DefaultCacheMaxBytes)
	if c.max != maxCacheItems {
		t.Fatalf("limit=%d", c.max)
	}
}

func TestCache_ByteBudgetEvictsOldest(t *testing.T) {
	a := Item{ID: "a", Summary: strings.Repeat("a", 400)}
	size := serializedItemSize(a)
	c := NewCache(time.Minute, 10, size*2)
	c.Put(a)
	c.Put(Item{ID: "b", Summary: strings.Repeat("b", 400)})
	c.Put(Item{ID: "c", Summary: strings.Repeat("c", 400)})
	stats := c.Stats()
	if stats.Items != 2 || stats.Bytes > stats.MaxBytes || stats.Evictions != 1 {
		t.Fatalf("stats=%+v", stats)
	}
	if _, ok := c.Get("a"); ok {
		t.Fatal("oldest item survived the byte budget")
	}
	if _, ok := c.Get("c"); !ok {
		t.Fatal("newest item was evicted")
	}
}

func TestCache_ReplacementAdjustsBytes(t *testing.T) {
	a := Item{ID: "a", Summary: strings.Repeat("a", 100)}
	c := NewCache(time.Minute, 10, 4096)
	c.Put(a)
	first := c.Stats().Bytes
	replacement := a
	replacement.Summary = strings.Repeat("z", 200)
	c.Put(replacement)
	stats := c.Stats()
	if stats.Items != 1 || stats.Bytes == first {
		t.Fatalf("replacement accounting failed: first=%d stats=%+v", first, stats)
	}
	got, ok := c.Get("a")
	if !ok || got.Summary != replacement.Summary {
		t.Fatalf("replacement not retained: %+v", got)
	}
}

func TestCache_OversizedItemCannotBypassBudget(t *testing.T) {
	item := Item{ID: "large", Summary: strings.Repeat("x", 500)}
	size := serializedItemSize(item)
	c := NewCache(time.Minute, 10, size-1)
	c.Put(item)
	stats := c.Stats()
	if stats.Items != 0 || stats.Bytes != 0 || stats.Oversized != 1 {
		t.Fatalf("stats=%+v", stats)
	}
	if _, ok := c.Get("large"); ok {
		t.Fatal("oversized item was stored")
	}
}

func TestCache_ExpiryAdjustsBytes(t *testing.T) {
	c := NewCache(20*time.Millisecond, 10, 4096)
	c.Put(Item{ID: "a", Summary: "value"})
	if c.Stats().Bytes == 0 {
		t.Fatal("item was not accounted")
	}
	time.Sleep(30 * time.Millisecond)
	stats := c.Stats()
	if stats.Items != 0 || stats.Bytes != 0 || stats.Expired != 1 {
		t.Fatalf("stats=%+v", stats)
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
	// For equal timestamps, put items in order by source and then by id.
	if items[0].ID != "b" || items[1].ID != "a" || items[2].ID != "c" {
		t.Fatalf("%+v", items)
	}
}

func TestSortItems_ServiceIDPrecedesOpaqueID(t *testing.T) {
	t0 := time.Unix(100, 0).UTC()
	for attempt := 0; attempt < 20; attempt++ {
		items := []Item{
			{ID: NewOpaqueID("ev"), ServiceID: "z-service", Source: SourceHealthchecks, SourceID: "check", ObservedAt: t0},
			{ID: NewOpaqueID("ev"), ServiceID: "a-service", Source: SourceHealthchecks, SourceID: "check", ObservedAt: t0},
		}
		SortItems(items)
		if items[0].ServiceID != "a-service" || items[1].ServiceID != "z-service" {
			t.Fatalf("order=%s,%s", items[0].ServiceID, items[1].ServiceID)
		}
		kept, truncated := KeepNewest(items, 1)
		if !truncated || len(kept) != 1 || kept[0].ServiceID != "z-service" {
			t.Fatalf("tie selection depended on an opaque id: %+v", kept)
		}
	}
}

func TestSortOutcomes_UsesServiceBinding(t *testing.T) {
	outcomes := []SourceOutcome{
		{Kind: SourceHealthchecks, SourceName: "hc", ServiceID: "z-service", Status: "error"},
		{Kind: SourceHealthchecks, SourceName: "hc", ServiceID: "a-service", Status: "ok"},
	}
	SortOutcomes(outcomes)
	if outcomes[0].ServiceID != "a-service" || outcomes[1].ServiceID != "z-service" {
		t.Fatalf("outcomes=%+v", outcomes)
	}
}
