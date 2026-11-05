package evidence

import (
	"encoding/json"
	"sync"
	"time"
)

const (
	maxCacheItems = 1000
	// DefaultCacheMaxBytes is the default serialized-payload budget.
	DefaultCacheMaxBytes int64 = 8 << 20
	// MaxCacheBytes is the hard serialized-payload ceiling.
	MaxCacheBytes int64 = 64 << 20
	// MinCacheBytes is the smallest accepted serialized-payload budget.
	MinCacheBytes int64 = 4 << 10
)

// Cache temporarily holds evidence items in-process.
// max sets the item limit and maxBytes sets the serialized-payload limit.
type Cache struct {
	mu        sync.Mutex
	ttl       time.Duration
	max       int
	maxBytes  int64
	bytes     int64
	items     map[string]cacheEntry
	order     []string // insertion order used for eviction
	evictions uint64
	expired   uint64
	oversized uint64
}

type cacheEntry struct {
	item      Item
	expiresAt time.Time
	size      int64
}

// CacheStats gives non-sensitive cache counters. Bytes measures the JSON
// encoding of stored items, not exact process memory.
type CacheStats struct {
	Items     int    `json:"items"`
	Bytes     int64  `json:"bytes"`
	MaxItems  int    `json:"max_items"`
	MaxBytes  int64  `json:"max_bytes"`
	Evictions uint64 `json:"evictions"`
	Expired   uint64 `json:"expired"`
	Oversized uint64 `json:"oversized_rejected"`
}

// NewCache makes a cache. An item limit of zero deactivates storage.
func NewCache(ttl time.Duration, max int, maxBytes int64) *Cache {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	if max > maxCacheItems {
		max = maxCacheItems
	}
	if maxBytes <= 0 {
		maxBytes = DefaultCacheMaxBytes
	}
	if maxBytes > MaxCacheBytes {
		maxBytes = MaxCacheBytes
	}
	return &Cache{
		ttl:      ttl,
		max:      max,
		maxBytes: maxBytes,
		items:    make(map[string]cacheEntry),
	}
}

// Put stores an evidence item by its id. An item larger than the whole byte
// budget is not stored. A replacement uses the new insertion position.
func (c *Cache) Put(item Item) {
	if c == nil || c.max <= 0 || item.ID == "" {
		return
	}
	size := serializedItemSize(item)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expireLocked(time.Now())
	if size > c.maxBytes {
		c.removeLocked(item.ID)
		c.oversized++
		return
	}
	c.removeLocked(item.ID)
	c.items[item.ID] = cacheEntry{item: item, expiresAt: time.Now().Add(c.ttl), size: size}
	c.order = append(c.order, item.ID)
	c.bytes += size
	for (len(c.items) > c.max || c.bytes > c.maxBytes) && len(c.order) > 0 {
		c.removeLocked(c.order[0])
		c.evictions++
	}
}

// PutAll stores multiple evidence items.
func (c *Cache) PutAll(items []Item) {
	for _, it := range items {
		c.Put(it)
	}
}

// Get gives a non-expired evidence item.
func (c *Cache) Get(id string) (Item, bool) {
	if c == nil || id == "" {
		return Item{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expireLocked(time.Now())
	e, ok := c.items[id]
	if !ok {
		return Item{}, false
	}
	it := e.item
	it.Freshness = FreshnessCached
	return it, true
}

// Stats gives counters for the retained cache state.
func (c *Cache) Stats() CacheStats {
	if c == nil {
		return CacheStats{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expireLocked(time.Now())
	return CacheStats{
		Items:     len(c.items),
		Bytes:     c.bytes,
		MaxItems:  c.max,
		MaxBytes:  c.maxBytes,
		Evictions: c.evictions,
		Expired:   c.expired,
		Oversized: c.oversized,
	}
}

func (c *Cache) expireLocked(now time.Time) {
	for id, e := range c.items {
		if now.After(e.expiresAt) {
			c.removeLocked(id)
			c.expired++
		}
	}
}

func (c *Cache) removeLocked(id string) {
	e, ok := c.items[id]
	if !ok {
		return
	}
	c.bytes -= e.size
	delete(c.items, id)
	n := 0
	for _, existing := range c.order {
		if existing != id {
			c.order[n] = existing
			n++
		}
	}
	c.order = c.order[:n]
}

func serializedItemSize(item Item) int64 {
	raw, err := json.Marshal(item)
	if err != nil {
		return 0
	}
	return int64(len(raw))
}
