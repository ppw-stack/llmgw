// Package health tracks the availability of upstream instances. A failing
// instance is skipped for a cooldown window so the next request does not pay
// the same timeout again.
package health

import (
	"sync"
	"time"
)

// Breaker keeps a failure counter per instance and opens the circuit for a
// cooldown window once the counter crosses the threshold. The state lives
// outside the routing snapshot on purpose: it must survive a configuration
// reload, otherwise a failing instance would come back to life every time
// the configuration is touched.
type Breaker struct {
	mu     sync.Mutex
	states map[string]*state
	now    func() time.Time
}

type state struct {
	failures  int
	openUntil time.Time
}

// NewBreaker returns a breaker with an empty state.
func NewBreaker() *Breaker {
	return &Breaker{states: map[string]*state{}, now: time.Now}
}

// Allow reports whether the instance may be used right now.
func (b *Breaker) Allow(instance string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	st, ok := b.states[instance]
	if !ok {
		return true
	}
	return !b.now().Before(st.openUntil)
}

// RecordFailure counts a failed attempt and opens the circuit once the
// threshold is reached. The counter restarts after the cooldown so the
// instance is probed again instead of being skipped forever.
func (b *Breaker) RecordFailure(instance string, maxFails int, cooldown time.Duration) {
	if maxFails <= 0 {
		maxFails = 1
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	st, ok := b.states[instance]
	if !ok {
		st = &state{}
		b.states[instance] = st
	}
	st.failures++
	if st.failures >= maxFails {
		st.failures = 0
		st.openUntil = b.now().Add(cooldown)
	}
}

// RecordSuccess clears the failure counter of an instance.
func (b *Breaker) RecordSuccess(instance string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if st, ok := b.states[instance]; ok {
		st.failures = 0
		st.openUntil = time.Time{}
	}
}

// Open reports whether the instance is currently skipped.
func (b *Breaker) Open(instance string) bool {
	return !b.Allow(instance)
}
