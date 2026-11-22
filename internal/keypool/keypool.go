package keypool

import (
	"sync"
	"sync/atomic"
)

// Pool rotates through the upstream keys of one provider.
// Keys are shared by every endpoint of that provider.
type Pool struct {
	keys    []string
	invalid []atomic.Bool
	next    atomic.Uint64
}

func New(keys []string) *Pool {
	p := &Pool{keys: append([]string(nil), keys...)}
	p.invalid = make([]atomic.Bool, len(p.keys))
	return p
}

func (p *Pool) Len() int { return len(p.keys) }

// Next returns the next usable key. ok is false when every key is invalid.
func (p *Pool) Next() (index int, key string, ok bool) {
	n := len(p.keys)
	if n == 0 {
		return -1, "", true // upstream without auth
	}
	start := int(p.next.Add(1))
	for i := 0; i < n; i++ {
		idx := (start + i) % n
		if !p.invalid[idx].Load() {
			return idx, p.keys[idx], true
		}
	}
	return -1, "", false
}

func (p *Pool) Invalidate(index int) {
	if index >= 0 && index < len(p.invalid) {
		p.invalid[index].Store(true)
	}
}

func (p *Pool) Available() int {
	n := 0
	for i := range p.invalid {
		if !p.invalid[i].Load() {
			n++
		}
	}
	return n
}

func (p *Pool) sameKeys(keys []string) bool {
	if len(keys) != len(p.keys) {
		return false
	}
	for i := range keys {
		if keys[i] != p.keys[i] {
			return false
		}
	}
	return true
}

// Manager keeps pools alive across config reloads so that invalid
// keys do not silently come back to life.
type Manager struct {
	mu    sync.Mutex
	pools map[string]*Pool
}

func NewManager() *Manager { return &Manager{pools: map[string]*Pool{}} }

func (m *Manager) Get(provider string, keys []string) *Pool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.pools[provider]; ok && p.sameKeys(keys) {
		return p
	}
	p := New(keys)
	m.pools[provider] = p
	return p
}
