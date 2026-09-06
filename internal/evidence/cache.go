package evidence

import (
	"sync"
	"time"
)

const maxCacheItems = 1000

// Cache temporarily holds evidence items in-process. max sets the item limit.
type Cache struct {
	mu    sync.Mutex
	ttl   time.Duration
	max   int
	items map[string]cacheEntry
	order []string // insertion order used for eviction
}

type cacheEntry struct {
	item      Item
	expiresAt time.Time
}

// NewCache makes a cache. max <= 0 deactivates storage.
func NewCache(ttl time.Duration, max int) *Cache {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	if max > maxCacheItems {
		max = maxCacheItems
	}
	return &Cache{
		ttl:   ttl,
		max:   max,
		items: make(map[string]cacheEntry),
	}
}

// Put stores an evidence item by its id.
func (c *Cache) Put(item Item) {
	if c == nil || c.max <= 0 || item.ID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expireLocked(time.Now())
	if _, exists := c.items[item.ID]; !exists {
		c.order = append(c.order, item.ID)
	}
	c.items[item.ID] = cacheEntry{item: item, expiresAt: time.Now().Add(c.ttl)}
	for len(c.items) > c.max && len(c.order) > 0 {
		old := c.order[0]
		c.order = c.order[1:]
		delete(c.items, old)
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

func (c *Cache) expireLocked(now time.Time) {
	for id, e := range c.items {
		if now.After(e.expiresAt) {
			delete(c.items, id)
		}
	}
	// Remove expired IDs from the order.
	n := 0
	for _, id := range c.order {
		if _, ok := c.items[id]; ok {
			c.order[n] = id
			n++
		}
	}
	c.order = c.order[:n]
}
