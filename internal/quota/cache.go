package quota

import (
	"sync"
	"time"
)

// Cache keeps the last report of an instance for a short while.
type Cache struct {
	mu    sync.Mutex
	items map[int64]cacheItem
	locks map[int64]*sync.Mutex
}

type cacheItem struct {
	report Report
	at     time.Time
}

// NewCache returns an empty cache.
func NewCache() *Cache {
	return &Cache{
		items: map[int64]cacheItem{},
		locks: map[int64]*sync.Mutex{},
	}
}

// get returns a report that is younger than the TTL.
func (c *Cache) get(id int64, ttl time.Duration) (Report, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	item, ok := c.items[id]
	if !ok || time.Since(item.at) > ttl {
		return Report{}, false
	}
	return item.report, true
}

// set stores the report of an instance.
func (c *Cache) set(id int64, report Report) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[id] = cacheItem{report: report, at: time.Now()}
}

// lock serialises the upstream query of one instance, so a burst of console
// clients triggers a single request. The returned function releases it.
func (c *Cache) lock(id int64) func() {
	c.mu.Lock()
	mu, ok := c.locks[id]
	if !ok {
		mu = &sync.Mutex{}
		c.locks[id] = mu
	}
	c.mu.Unlock()
	mu.Lock()
	return mu.Unlock
}
