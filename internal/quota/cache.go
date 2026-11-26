package quota

import (
	"sync"
	"time"
)

// Cache keeps the last report of an instance for a short while.
type Cache struct {
	mu     sync.Mutex
	items  map[int64]cacheItem
	byName map[string]int64
	locks  map[int64]*sync.Mutex
}

type cacheItem struct {
	report Report
	at     time.Time
}

// NewCache returns an empty cache.
func NewCache() *Cache {
	return &Cache{
		items:  map[int64]cacheItem{},
		byName: map[string]int64{},
		locks:  map[int64]*sync.Mutex{},
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
	if prev, ok := c.items[id]; ok && prev.report.Instance != "" && prev.report.Instance != report.Instance {
		if c.byName[prev.report.Instance] == id {
			delete(c.byName, prev.report.Instance)
		}
	}
	c.items[id] = cacheItem{report: report, at: time.Now()}
	if report.Instance != "" {
		c.byName[report.Instance] = id
	}
}

// stateByName returns the peak window usage percentage and whether any window
// is exhausted for the named instance, provided a fresh, error-free report
// exists within maxAge.
func (c *Cache) stateByName(name string, maxAge time.Duration) (maxUsedPct float64, exhausted bool, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id, exists := c.byName[name]
	if !exists {
		return 0, false, false
	}
	item, exists := c.items[id]
	if !exists || time.Since(item.at) > maxAge {
		return 0, false, false
	}
	if !item.report.Supported || item.report.Error != "" || len(item.report.Windows) == 0 {
		return 0, false, false
	}
	for _, w := range item.report.Windows {
		if w.UsedPct > maxUsedPct {
			maxUsedPct = w.UsedPct
		}
		if w.Status == StatusExhausted || w.UsedPct >= 100 {
			exhausted = true
		}
	}
	return maxUsedPct, exhausted, true
}

// delete removes the cached report of one instance so the next query fetches
// fresh data from the upstream.
//
// It takes the per-instance lock as well, so an in-flight query cannot write
// its stale report back after the entry has been dropped.
func (c *Cache) delete(id int64) {
	unlock := c.lock(id)
	defer unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if prev, ok := c.items[id]; ok && prev.report.Instance != "" {
		if c.byName[prev.report.Instance] == id {
			delete(c.byName, prev.report.Instance)
		}
	}
	delete(c.items, id)
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
