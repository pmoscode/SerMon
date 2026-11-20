package webapi

import (
	"errors"
	"sync"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/authz"
)

// Limits of the live streams (WatchMetrics). A stream holds a goroutine and queries the ring
// buffer every second, so their number is bounded per session and per instance.
const (
	// MaxLiveStreamsPerSession allows a few live views in several tabs of one browser.
	MaxLiveStreamsPerSession = 32
	// MaxLiveStreams bounds all live streams of the instance.
	MaxLiveStreams = 1000
	// streamRecheck is how often a running stream checks that its session and rights still hold.
	streamRecheck = 30 * time.Second
)

var (
	errTooManyStreams = errors.New("too many live views open, close some and try again")
	// errScopeChanged ends a stream whose caller's rights changed; the client opens a new one.
	errScopeChanged = errors.New("rights changed")
)

// streamLimiter counts the open live streams per session and in total.
type streamLimiter struct {
	mu                   sync.Mutex
	perSession           map[string]int
	total                int
	maxPerSession, limit int
}

func newStreamLimiter(maxPerSession, limit int) *streamLimiter {
	return &streamLimiter{perSession: map[string]int{}, maxPerSession: maxPerSession, limit: limit}
}

// acquire takes a slot for the session; false if a limit is reached.
func (l *streamLimiter) acquire(session string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.total >= l.limit || l.perSession[session] >= l.maxPerSession {
		return false
	}
	l.perSession[session]++
	l.total++
	return true
}

// release frees a slot taken with acquire.
func (l *streamLimiter) release(session string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.total--
	if l.perSession[session]--; l.perSession[session] <= 0 {
		delete(l.perSession, session)
	}
}

// reauthenticator checks the session of a running stream again and returns the current principal.
type reauthenticator func() (authz.Principal, error)

type reauthKey struct{}

// samePrincipal reports whether two principals have the same rights (bindings in any order).
func samePrincipal(a, b authz.Principal) bool {
	if a.User != b.User || a.Org != b.Org || a.Superadmin != b.Superadmin || len(a.Bindings) != len(b.Bindings) {
		return false
	}
	for _, binding := range a.Bindings {
		found := false
		for _, other := range b.Bindings {
			if binding == other {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
