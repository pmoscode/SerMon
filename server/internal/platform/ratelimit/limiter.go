// Package ratelimit provides an in-memory sliding-window rate limiter keyed by e.g. client IP
// (brute-force protection for login and enrollment, KONZEPT §7, §18.1).
//
// State is per process; with several server instances each instance limits on its own, which is
// acceptable for the brute-force use case (the per-account lockout is stored in the database).
package ratelimit

import (
	"net/netip"
	"sync"
	"time"
)

// Limiter allows at most limit events per key within a sliding window.
type Limiter struct {
	mu          sync.Mutex
	limit       int
	window      time.Duration
	now         func() time.Time
	events      map[string][]time.Time
	lastCleanup time.Time
}

// New creates a Limiter. limit and window must be positive.
func New(limit int, window time.Duration, now func() time.Time) *Limiter {
	if limit <= 0 || window <= 0 {
		panic("ratelimit: limit and window must be positive")
	}
	return &Limiter{limit: limit, window: window, now: now, events: map[string][]time.Time{}, lastCleanup: now()}
}

// Allow records an event for key and reports whether it is within the limit.
// Denied events are not recorded, so hammering does not extend the block.
func (l *Limiter) Allow(key string) bool {
	key = normalize(key)
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	if now.Sub(l.lastCleanup) >= l.window {
		l.cleanup(now)
	}
	recent := l.recent(key, now)
	if len(recent) >= l.limit {
		l.events[key] = recent
		return false
	}
	l.events[key] = append(recent, now)
	return true
}

// Len returns the number of tracked keys (for tests and metrics).
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.events)
}

func (l *Limiter) recent(key string, now time.Time) []time.Time {
	events := l.events[key]
	i := 0
	for i < len(events) && now.Sub(events[i]) >= l.window {
		i++
	}
	return events[i:]
}

func (l *Limiter) cleanup(now time.Time) {
	for key := range l.events {
		if recent := l.recent(key, now); len(recent) == 0 {
			delete(l.events, key)
		} else {
			l.events[key] = recent
		}
	}
	l.lastCleanup = now
}

// normalize counts an IPv6 client per /64: that is what one host or customer usually controls,
// per address the client would get 2^64 budgets. IPv4 (also IPv4-mapped) and other keys stay.
func normalize(key string) string {
	addr, err := netip.ParseAddr(key)
	if err != nil {
		return key
	}
	addr = addr.Unmap()
	if addr.Is4() {
		return addr.String()
	}
	prefix, err := addr.Prefix(64)
	if err != nil {
		return key
	}
	return prefix.String()
}
