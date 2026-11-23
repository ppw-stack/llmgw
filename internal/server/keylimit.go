package server

import (
	"sync"
	"time"
)

// keyLimiter applies a per-gateway-key request budget and an in-flight request
// cap. The gateway is the only thing standing between one leaked key and an
// operator's upstream bill, so the data plane cannot be unlimited by default.
//
// Zero values disable a dimension: rpm 0 means no rate limit, maxConcurrent 0
// means no concurrency cap.
type keyLimiter struct {
	mu     sync.Mutex
	rpm    int
	max    int
	now    func() time.Time
	states map[int64]*keyState
}

type keyState struct {
	windowStart time.Time
	count       int
	inflight    int
}

func newKeyLimiter(rpm, maxConcurrent int) *keyLimiter {
	return &keyLimiter{
		rpm:    rpm,
		max:    maxConcurrent,
		now:    time.Now,
		states: make(map[int64]*keyState),
	}
}

// setLimits updates the budget that applies to every key. The config watcher
// calls it on reload, so the operator can tighten or relax the limit without
// restarting the gateway.
func (l *keyLimiter) setLimits(rpm, maxConcurrent int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rpm = rpm
	l.max = maxConcurrent
}

// acquire reserves one request slot. It returns a release function that must be
// called when the request finishes, or ok=false with the delay to advertise in
// Retry-After.
func (l *keyLimiter) acquire(id int64) (release func(), ok bool, retry time.Duration) {
	if l == nil {
		return func() {}, true, 0
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	// Both dimensions off: nothing to reserve. The check lives under the
	// lock so a concurrent setLimits is never read half-applied.
	if l.rpm <= 0 && l.max <= 0 {
		return func() {}, true, 0
	}

	st := l.states[id]
	if st == nil {
		if len(l.states) >= 10000 {
			l.evictLocked(now)
		}
		st = &keyState{windowStart: now}
		l.states[id] = st
	}
	if l.rpm > 0 && now.Sub(st.windowStart) >= time.Minute {
		st.windowStart = now
		st.count = 0
	}
	if l.rpm > 0 && st.count >= l.rpm {
		return nil, false, time.Until(st.windowStart.Add(time.Minute))
	}
	if l.max > 0 && st.inflight >= l.max {
		return nil, false, time.Second
	}
	st.count++
	st.inflight++
	released := false
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if released {
			return
		}
		released = true
		if st.inflight > 0 {
			st.inflight--
		}
	}, true, 0
}

// evictLocked drops idle keys whose window elapsed, so the map cannot grow
// without bound when many short-lived keys are used.
func (l *keyLimiter) evictLocked(now time.Time) {
	for id, st := range l.states {
		if st.inflight == 0 && now.Sub(st.windowStart) >= time.Minute {
			delete(l.states, id)
		}
	}
}

// inflight reports the current in-flight count of one key. Tests use it.
func (l *keyLimiter) inflight(id int64) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if st := l.states[id]; st != nil {
		return st.inflight
	}
	return 0
}
