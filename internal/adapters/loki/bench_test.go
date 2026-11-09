package loki

import "testing"

func BenchmarkParseNano(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = parseNano("-1000000000")
	}
}
