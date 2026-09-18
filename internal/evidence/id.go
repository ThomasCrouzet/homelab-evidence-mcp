package evidence

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync/atomic"
)

var idCounter atomic.Uint64

// NewOpaqueID gives a process-local opaque identifier.
func NewOpaqueID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	n := idCounter.Add(1)
	return fmt.Sprintf("%s_%d_%s", prefix, n, hex.EncodeToString(b[:]))
}
