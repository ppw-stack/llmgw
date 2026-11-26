package server

import (
	"sync"

	"github.com/pw/llmgw/internal/store"
)

// keyCache keeps gateway keys in memory so the request path never touches the
// database. It is authoritative for authentication; every admin mutation
// updates it in the same critical section as the database write.
type keyCache struct {
	mu     sync.RWMutex
	byHash map[string]store.APIKey
	byID   map[int64]string
}

func newKeyCache(st *store.Store) (*keyCache, error) {
	c := &keyCache{
		byHash: make(map[string]store.APIKey),
		byID:   make(map[int64]string),
	}
	records, err := st.KeyRecords()
	if err != nil {
		return nil, err
	}
	for _, rec := range records {
		c.byHash[rec.Hash] = rec.Key
		c.byID[rec.Key.ID] = rec.Hash
	}
	return c, nil
}

func (c *keyCache) get(hash string) (store.APIKey, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	k, ok := c.byHash[hash]
	return k, ok
}

func (c *keyCache) put(hash string, k store.APIKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byHash[hash] = k
	c.byID[k.ID] = hash
}

func (c *keyCache) update(k store.APIKey) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	hash, ok := c.byID[k.ID]
	if !ok {
		return false
	}
	c.byHash[hash] = k
	return true
}

func (c *keyCache) setEnabled(id int64, enabled bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	hash, ok := c.byID[id]
	if !ok {
		return false
	}
	k := c.byHash[hash]
	k.Enabled = enabled
	c.byHash[hash] = k
	return true
}

func (c *keyCache) delete(id int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if hash, ok := c.byID[id]; ok {
		delete(c.byHash, hash)
		delete(c.byID, id)
	}
}

func (c *keyCache) len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.byHash)
}
