package middleware

import (
	"sync"
	"time"
)

// RateLimiter is a tiny in-memory token-bucket limiter keyed by an
// arbitrary string (phone, IP, user ID).
//
// We deliberately keep it process-local rather than reaching for Redis:
// (a) the OTP send + verify flow is the only caller, (b) Railway runs a
// single backend instance today, and (c) "up to N sends per window per
// instance" is a fine guarantee for this purpose. If we ever scale
// horizontally, swap the implementation for a Redis-backed one without
// changing the call site.
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	window  time.Duration
	limit   int
}

type bucket struct {
	count int
	reset time.Time
}

// NewRateLimiter creates a limiter that allows `limit` events per
// `window` per key.
func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		buckets: make(map[string]*bucket),
		window:  window,
		limit:   limit,
	}
}

// Allow returns true if `key` is under the limit and consumes one
// token. False means the caller should reject the request.
func (l *RateLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	b, ok := l.buckets[key]
	if !ok || now.After(b.reset) {
		l.buckets[key] = &bucket{count: 1, reset: now.Add(l.window)}
		return true
	}
	if b.count >= l.limit {
		return false
	}
	b.count++
	return true
}
