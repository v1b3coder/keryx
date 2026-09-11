// Package ratelimit provides per-key token buckets for the relay's limits
// (§5.3): per-publisher publish rate (default 60/min, burst 120) and global
// per-IP throttling of subscription registration.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter is a set of token buckets keyed by string.
type Limiter struct {
	mu      sync.Mutex
	rate    float64 // tokens per second
	burst   float64
	now     func() time.Time
	buckets map[string]*bucket
}

type bucket struct {
	tokens   float64
	last     time.Time
	lastSeen time.Time
}

// New returns a Limiter refilled at perMin tokens per minute, up to burst.
func New(perMin, burst int) *Limiter {
	return &Limiter{
		rate:    float64(perMin) / 60.0,
		burst:   float64(burst),
		now:     time.Now,
		buckets: make(map[string]*bucket),
	}
}

// Allow reports whether one token is available for key and consumes it if so.
func (l *Limiter) Allow(key string) bool {
	return l.AllowN(key, 1)
}

// AllowN reports whether n tokens are available for key and consumes them if
// so (all-or-nothing).
func (l *Limiter) AllowN(key string, n int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: l.now()}
		l.buckets[key] = b
	}
	now := l.now()
	b.tokens += now.Sub(b.last).Seconds() * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
	b.lastSeen = now
	if b.tokens < float64(n) {
		return false
	}
	b.tokens -= float64(n)
	return true
}

// Cleanup drops buckets idle for longer than idle. Bounded key space matters
// for per-IP throttling; call it periodically from the server.
func (l *Limiter) Cleanup(idle time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := l.now().Add(-idle)
	for k, b := range l.buckets {
		if b.lastSeen.Before(cutoff) {
			delete(l.buckets, k)
		}
	}
}
