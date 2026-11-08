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
	coalesced       int
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

func (i *RequestInfo) recordCoalesced(collectedAt time.Time) {
	if i == nil {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.requests++
	i.coalesced++
	if i.oldestCollected.IsZero() || collectedAt.Before(i.oldestCollected) {
		i.oldestCollected = collectedAt
	}
}

// recordMissTime records the collection time of a fresh response. With a
// mixed hit and miss collection, it gives the age of the oldest page.
func (i *RequestInfo) recordMissTime(collectedAt time.Time) {
	if i == nil {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.oldestCollected.IsZero() || collectedAt.Before(i.oldestCollected) {
		i.oldestCollected = collectedAt
	}
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
	age := time.Since(i.oldestCollected)
	if age < 0 {
		age = 0
	}
	ageS := int64(age / time.Second)
	switch {
	case i.requests == 0 && i.disabled:
		return "disabled", 0
	case i.requests == 0:
		return "", 0
	case i.hits == i.requests:
		return "hit", ageS
	case i.misses == i.requests:
		return "miss", ageS
	case i.coalesced == i.requests:
		return "coalesced", ageS
	default:
		return "mixed", ageS
	}
}
