package httpx

import "sync"

const (
	// DefaultSourceCacheMaxBytes is the shared source response cache budget
	// when the configuration does not set one.
	DefaultSourceCacheMaxBytes int64 = 32 << 20
	// MinSourceCacheMaxBytes is the smallest explicit configuration value.
	MinSourceCacheMaxBytes int64 = 64 << 10
	// MaxSourceCacheMaxBytes is the largest explicit configuration value.
	MaxSourceCacheMaxBytes int64 = 256 << 20
)

// CacheBudget bounds cached response-body bytes for every client that shares
// it. The budget stores no body and performs no lookup. It authorizes, expires,
// and evicts storage only.
type CacheBudget struct {
	mu        sync.Mutex
	limit     int64
	used      int64
	evictions uint64
	clients   []*LockedClient
	order     []budgetEntry
}

type budgetEntry struct {
	client *LockedClient
	key    string
}

// NewCacheBudget makes a budget. A limit at or below zero uses the default.
func NewCacheBudget(limit int64) *CacheBudget {
	if limit <= 0 {
		limit = DefaultSourceCacheMaxBytes
	}
	return &CacheBudget{limit: limit}
}

// Limit gives the effective byte limit. The value cannot change after
// construction. A nil budget gives the default.
func (b *CacheBudget) Limit() int64 {
	if b == nil {
		return DefaultSourceCacheMaxBytes
	}
	return b.limit
}

func (b *CacheBudget) register(client *LockedClient) {
	if b == nil || client == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, existing := range b.clients {
		if existing == client {
			return
		}
	}
	b.clients = append(b.clients, client)
}

// expireAllLocked removes expired entries from every client that shares the
// budget. The caller holds b.mu. Each client uses its own clock.
func (b *CacheBudget) expireAllLocked() {
	for _, client := range b.clients {
		client.mu.Lock()
		client.expireCacheLocked(client.clock())
		client.mu.Unlock()
	}
}

// removeOrderLocked removes the global insertion record for one entry. The
// caller holds b.mu.
func (b *CacheBudget) removeOrderLocked(client *LockedClient, key string) {
	n := 0
	for _, entry := range b.order {
		if entry.client == client && entry.key == key {
			continue
		}
		b.order[n] = entry
		n++
	}
	b.order = b.order[:n]
}
