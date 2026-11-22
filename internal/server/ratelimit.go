package server

import (
	"sync"
	"time"
)

// loginLimiter throttles failed admin login attempts per client IP.
type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string]*attemptRecord
	window   time.Duration
	max      int
}

type attemptRecord struct {
	count int
	reset time.Time
}

func newLoginLimiter(window time.Duration, max int) *loginLimiter {
	return &loginLimiter{
		attempts: map[string]*attemptRecord{},
		window:   window,
		max:      max,
	}
}

func (l *loginLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	rec, ok := l.attempts[key]
	if !ok {
		return true
	}
	if time.Now().After(rec.reset) {
		delete(l.attempts, key)
		return true
	}
	return rec.count < l.max
}

func (l *loginLimiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	rec, ok := l.attempts[key]
	if !ok || time.Now().After(rec.reset) {
		l.attempts[key] = &attemptRecord{count: 1, reset: time.Now().Add(l.window)}
		return
	}
	rec.count++
}

func (l *loginLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, key)
}

// retryAfter reports how long the caller should wait before trying again.
func (l *loginLimiter) retryAfter(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if rec, ok := l.attempts[key]; ok {
		if d := time.Until(rec.reset); d > 0 {
			return d
		}
	}
	return 0
}
