package evidence

import (
	"sync"
	"time"
)

const maxCacheItems = 1000

// Cache conserve temporairement un nombre borné de preuves dans le processus.
type Cache struct {
	mu    sync.Mutex
	ttl   time.Duration
	max   int
	items map[string]cacheEntry
	order []string // ordre d’insertion utilisé pour l’éviction
}

type cacheEntry struct {
	item      Item
	expiresAt time.Time
}

// NewCache crée un cache ; max <= 0 désactive le stockage.
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

// Put stocke une preuve selon son identifiant.
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

// PutAll stocke plusieurs preuves.
func (c *Cache) PutAll(items []Item) {
	for _, it := range items {
		c.Put(it)
	}
}

// Get renvoie une preuve non expirée.
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
	// Compacter l’ordre après expiration.
	n := 0
	for _, id := range c.order {
		if _, ok := c.items[id]; ok {
			c.order[n] = id
			n++
		}
	}
	c.order = c.order[:n]
}
