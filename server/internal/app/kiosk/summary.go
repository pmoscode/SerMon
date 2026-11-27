package kiosk

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/fleet"
)

// SummaryFor is how long a display reuses the fleet's summary it read (V1-13): a refresh reads
// every widget of the dashboard, each needs the visible nodes, and the summary is the costliest
// read of them. Well below the refresh of a display (30 s), so every refresh sees the fleet anew.
const SummaryFor = 10 * time.Second

// summaries keeps the last summary of each token with the rights it was read with. A summary
// read with other rights is never used: the rights are loaded with every call, so a change of
// them takes effect at once. Calls at the same time wait for one read.
type summaries struct {
	mu      sync.Mutex
	entries map[TokenID]*summaryEntry
}

type summaryEntry struct {
	rights  string
	at      time.Time
	done    chan struct{}
	summary fleet.Summary
	err     error
}

// get returns the summary of token for c, read with read unless one read with the same rights
// less than SummaryFor ago (or being read) is there.
func (s *summaries) get(ctx context.Context, token TokenID, c access.Caller, now time.Time, read func() (fleet.Summary, error)) (fleet.Summary, error) {
	rights := fingerprint(c.Principal)
	s.mu.Lock()
	if s.entries == nil {
		s.entries = map[TokenID]*summaryEntry{}
	}
	e, ok := s.entries[token]
	if !ok || e.rights != rights || now.Sub(e.at) >= SummaryFor || now.Before(e.at) {
		e = &summaryEntry{rights: rights, at: now, done: make(chan struct{})}
		s.entries[token] = e
		s.prune(now)
		s.mu.Unlock()
		e.summary, e.err = read()
		close(e.done)
		if e.err != nil {
			s.forget(token, e)
		}
	} else {
		s.mu.Unlock()
	}
	select {
	case <-e.done:
	case <-ctx.Done():
		return fleet.Summary{}, fmt.Errorf("kiosk fleet: %w", ctx.Err())
	}
	if e.err != nil {
		return fleet.Summary{}, e.err
	}
	out := e.summary
	out.Nodes = slices.Clone(e.summary.Nodes)
	return out, nil
}

// prune drops the summaries older than SummaryFor; s.mu is held.
func (s *summaries) prune(now time.Time) {
	for token, e := range s.entries {
		if now.Sub(e.at) >= SummaryFor {
			select {
			case <-e.done:
				delete(s.entries, token)
			default:
			}
		}
	}
}

func (s *summaries) forget(token TokenID, e *summaryEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries[token] == e {
		delete(s.entries, token)
	}
}

// fingerprint names the rights of p: equal for the same rights in any order of the bindings.
func fingerprint(p authz.Principal) string {
	bindings := slices.Clone(p.Bindings)
	slices.SortFunc(bindings, func(a, b authz.Binding) int {
		return cmp.Or(
			strings.Compare(a.Group.String(), b.Group.String()),
			cmp.Compare(boolInt(a.AllGroups), boolInt(b.AllGroups)),
			cmp.Compare(a.Role, b.Role),
		)
	})
	return fmt.Sprintf("%s/%s/%t/%t/%v", p.User, p.Org, p.Superadmin, p.ViewersWithoutLogs, bindings)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
