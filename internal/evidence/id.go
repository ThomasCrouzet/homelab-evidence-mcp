package evidence

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync/atomic"
)

var idCounter atomic.Uint64

// NewOpaqueID renvoie un identifiant opaque local au processus.
func NewOpaqueID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	n := idCounter.Add(1)
	return fmt.Sprintf("%s_%d_%s", prefix, n, hex.EncodeToString(b[:]))
}
