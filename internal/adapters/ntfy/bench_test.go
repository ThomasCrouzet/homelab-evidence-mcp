package ntfy

import (
	"fmt"
	"strings"
	"testing"
)

func BenchmarkDecodeMessages1000(b *testing.B) {
	var builder strings.Builder
	for index := 0; index < 1000; index++ {
		_, _ = fmt.Fprintf(&builder, `{"id":"%d","time":%d,"event":"message","topic":"alerts","message":"line %d"}`, index, 1_800_000_000+index, index)
		builder.WriteByte('\n')
	}
	body := []byte(builder.String())
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = decodeMessages(body, false)
	}
}
