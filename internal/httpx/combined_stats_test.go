package httpx

import (
	"testing"
	"time"
)

func TestCombinedStats_SumsCoalesced(t *testing.T) {
	first := NewLockedClient(Options{CacheTTL: time.Minute})
	first.hits.Store(2)
	first.misses.Store(3)
	first.coalesced.Store(5)
	second := NewLockedClient(Options{CacheTTL: time.Minute})
	second.hits.Store(7)
	second.misses.Store(11)
	second.coalesced.Store(13)

	got := CombinedStats(nil, first, nil, second)
	if got.Coalesced != 18 {
		t.Fatalf("coalesced=%d, want 18", got.Coalesced)
	}
	if got.Hits != 9 || got.Misses != 14 {
		t.Fatalf("hits=%d misses=%d, want 9 and 14", got.Hits, got.Misses)
	}
}

func TestCombinedStats_ZeroAndOneClient(t *testing.T) {
	if got := CombinedStats(); got.Coalesced != 0 || got.Hits != 0 || got.Misses != 0 {
		t.Fatalf("empty stats=%+v", got)
	}
	client := NewLockedClient(Options{CacheTTL: time.Minute})
	client.coalesced.Store(4)
	if got := CombinedStats(client); got.Coalesced != 4 {
		t.Fatalf("single client coalesced=%d, want 4", got.Coalesced)
	}
	if got := CombinedStats(nil); got.Coalesced != 0 {
		t.Fatalf("nil client coalesced=%d, want 0", got.Coalesced)
	}
}
