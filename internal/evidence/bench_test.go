package evidence

import (
	"strconv"
	"testing"
	"time"
)

func benchmarkItems(count int) []Item {
	base := time.Unix(1_800_000_000, 0).UTC()
	items := make([]Item, count)
	for index := range items {
		items[index] = Item{
			ID:         NewOpaqueID("ev"),
			ServiceID:  "svc",
			Source:     SourceLoki,
			SourceID:   "stream-" + strconv.Itoa(index%8),
			ObservedAt: base.Add(time.Duration(index) * time.Millisecond),
			Attributes: map[string]any{"line": "level=info request served", "tags": []string{"a", "b"}},
			Order:      index,
		}
	}
	return items
}

func BenchmarkSortItems200(b *testing.B) {
	items := benchmarkItems(200)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SortItems(items)
	}
}

func BenchmarkKeepNewest200(b *testing.B) {
	items := benchmarkItems(200)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = KeepNewest(items, 50)
	}
}
