package webapi

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
)

// Limits of the live streams (WatchMetrics, TailLogs). A stream holds a goroutine and queries the
// ring buffer or Loki every few seconds, so their number is bounded per session, user,
// organization and instance.
const (
	// MaxLiveStreamsPerSession allows a few live views in several tabs of one browser.
	MaxLiveStreamsPerSession = 32
	// MaxLiveStreamsPerUser bounds a user with many sessions (several browsers, scripts).
	MaxLiveStreamsPerUser = 64
	// MaxLiveStreamsPerOrg keeps one organization from taking all streams of the instance.
	MaxLiveStreamsPerOrg = 250
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

// streamLimits are the maximum numbers of open streams.
type streamLimits struct{ Session, User, Org, Total int }

var defaultStreamLimits = streamLimits{
	Session: MaxLiveStreamsPerSession, User: MaxLiveStreamsPerUser, Org: MaxLiveStreamsPerOrg, Total: MaxLiveStreams,
}

// LiveStreams counts the open live streams of the web UI – WatchMetrics and TailLogs together –
// against one set of limits (T26).
type LiveStreams struct{ limiter *streamLimiter }

// NewLiveStreams returns the counter with the limits of T26.
func NewLiveStreams() *LiveStreams {
	return &LiveStreams{limiter: newStreamLimiter(defaultStreamLimits)}
}

// Open returns the number of open live streams (server metrics, ADR 0011).
func (l *LiveStreams) Open() int { return l.limiter.open() }

func (l *LiveStreams) orNew() *streamLimiter {
	if l == nil {
		return newStreamLimiter(defaultStreamLimits)
	}
	return l.limiter
}

// streamKey says whom a stream counts against.
type streamKey struct{ session, user, org string }

// streamLimiter counts the open live streams per session, user, organization and in total.
type streamLimiter struct {
	mu     sync.Mutex
	limits streamLimits
	counts map[string]int // by "s:", "u:" and "o:" keys
	total  int
}

func newStreamLimiter(limits streamLimits) *streamLimiter {
	return &streamLimiter{limits: limits, counts: map[string]int{}}
}

type streamCounter struct {
	key   string
	limit int
}

func (k streamKey) counters(l streamLimits) []streamCounter {
	return []streamCounter{{"s:" + k.session, l.Session}, {"u:" + k.user, l.User}, {"o:" + k.org, l.Org}}
}

// acquire takes a slot for the stream; false if a limit is reached.
func (l *streamLimiter) acquire(k streamKey) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.total >= l.limits.Total {
		return false
	}
	counters := k.counters(l.limits)
	for _, c := range counters {
		if l.counts[c.key] >= c.limit {
			return false
		}
	}
	for _, c := range counters {
		l.counts[c.key]++
	}
	l.total++
	return true
}

// open returns the number of open streams.
func (l *streamLimiter) open() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.total
}

// streamKeyOf is the stream key of a caller's session.
func streamKeyOf(s session.Session, c access.Caller) streamKey {
	return streamKey{session: s.ID, user: c.Principal.User.String(), org: c.Principal.Org.String()}
}

// recheckOf checks the session of a running stream again: an ended session fails it, changed
// rights end it (errScopeChanged; the client reconnects with the new scope).
func recheckOf(ctx context.Context, c access.Caller) func() error {
	reauth, _ := ctx.Value(reauthKey{}).(reauthenticator)
	return func() error {
		if reauth == nil {
			return nil
		}
		current, err := reauth()
		if err != nil {
			return err
		}
		if !samePrincipal(current, c.Principal) {
			return errScopeChanged
		}
		return nil
	}
}

// release frees a slot taken with acquire.
func (l *streamLimiter) release(k streamKey) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.total--
	for _, c := range k.counters(l.limits) {
		if l.counts[c.key]--; l.counts[c.key] <= 0 {
			delete(l.counts, c.key)
		}
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
