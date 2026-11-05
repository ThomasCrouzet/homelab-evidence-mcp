package httpx

import (
	"context"
	"sync"
	"time"
)

type requestInfoKey struct{}

// RequestInfo records source cache use for one collection. It is optional and
// never changes observation timestamps.
type RequestInfo struct {
	mu              sync.Mutex
	requests        int
	hits            int
	misses          int
	disabled        bool
	oldestCollected time.Time
}

// WithRequestInfo attaches a recorder to the context.
func WithRequestInfo(ctx context.Context) (context.Context, *RequestInfo) {
	info := &RequestInfo{}
	return context.WithValue(ctx, requestInfoKey{}, info), info
}

func requestInfoFrom(ctx context.Context) *RequestInfo {
	info, _ := ctx.Value(requestInfoKey{}).(*RequestInfo)
	return info
}

func (i *RequestInfo) recordHit(collectedAt time.Time) {
	if i == nil {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.requests++
	i.hits++
	if i.oldestCollected.IsZero() || collectedAt.Before(i.oldestCollected) {
		i.oldestCollected = collectedAt
	}
}

func (i *RequestInfo) recordMiss() {
	if i == nil {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.requests++
	i.misses++
}

func (i *RequestInfo) recordDisabled() {
	if i == nil {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.disabled = true
}

// Snapshot gives a stable cache state and the age of the oldest snapshot used.
func (i *RequestInfo) Snapshot() (string, int64) {
	if i == nil {
		return "", 0
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	switch {
	case i.requests == 0 && i.disabled:
		return "disabled", 0
	case i.requests == 0:
		return "", 0
	case i.hits == i.requests:
		age := time.Since(i.oldestCollected)
		if age < 0 {
			age = 0
		}
		return "hit", int64(age / time.Second)
	default:
		return "miss", 0
	}
}
