package alerting

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/secevent"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	"github.com/pmoscode/SerMon/server/internal/domain/textsafe"
)

// Event rules (KONZEPT §12.2, M2-08) count security events. The engine runs on whichever server
// instance holds the organization's lease, not where the events arrived, so each round reads the
// events received within the window from PostgreSQL (received_at) and counts them by when they
// occurred: a burst within a backlog an agent delivers after an outage fires on arrival, and a
// day of scattered failures arriving at once does not.

const (
	// MaxEventsRead bounds the events one query of a rule reads; a flood beyond it is counted on
	// the newest ones, and open instances keep their state.
	MaxEventsRead = 50_000
	// MaxEventInstances bounds the instances one event rule reports per round (the highest
	// counts first), so an attacker rotating addresses cannot flood the alert tables.
	MaxEventInstances = 100
	// MaxFieldBytes bounds a field value in a notification.
	MaxFieldBytes  = 256
	maxEventWindow = 24 * time.Hour
	maxLearning    = 30 * 24 * time.Hour
	maxEventCount  = 100_000
	maxMatchValue  = secevent.MaxUser
)

// EventQuery selects the events of one kind and the given nodes received at or after Since.
type EventQuery struct {
	Kind  secevent.Kind
	Nodes []identity.NodeID
	Since time.Time
	Limit int
}

// EventReader reads security events within a scope: newest received first, at most Limit.
type EventReader interface {
	Received(ctx context.Context, scope tenant.Scope, q EventQuery) ([]secevent.Event, error)
}

// CountOf is what an event rule counts per instance.
type CountOf int

// Counts: the events of a node (brute force), or the nodes on which the same group (IP) shows up
// (fleet correlation; every node involved gets an instance).
const (
	CountEvents CountOf = iota
	CountNodes
)

// EventCondition is what must precede an event for it to count: at least Count events of Kind
// with the same node and groupBy values within Window before it (a successful login after failed
// attempts from the same IP).
type EventCondition struct {
	Kind   secevent.Kind
	Count  int
	Window time.Duration
}

// EventRule counts the events of one kind per node and groupBy values (ip, user) within Window,
// optionally only those that match fields (user: root) or follow an EventCondition. The count
// is compared with Warning and Critical (at least so many; 0: unused). NotifyFields are copied
// from the latest event into the notification. Learning leaves out the events of a node within
// that time after SerMon began to learn it (fleet.Node.LearningSince: on a new node every login
// source is new).
type EventRule struct {
	Kind              secevent.Kind
	Match             map[string]string
	GroupBy           []string
	Count             CountOf
	Window            time.Duration
	Warning, Critical int
	After             *EventCondition
	NotifyFields      []string
	Learning          time.Duration
}

func validEventWindow(name string, w time.Duration) error {
	if w < time.Minute || w > maxEventWindow {
		return fmt.Errorf("%w: %s must be between 1m and %v", alert.ErrInvalidRule, name, maxEventWindow)
	}
	return nil
}

// Validate checks the rule against the fields of the event kinds.
func (r EventRule) Validate() error {
	if _, err := secevent.ParseKind(string(r.Kind)); err != nil {
		return fmt.Errorf("%w: event %q is no security event", alert.ErrInvalidRule, r.Kind)
	}
	switch {
	case r.Warning < 0 || r.Critical < 0 || r.Warning > maxEventCount || r.Critical > maxEventCount:
		return fmt.Errorf("%w: counts must be between 1 and %d", alert.ErrInvalidRule, maxEventCount)
	case r.Warning == 0 && r.Critical == 0:
		return fmt.Errorf("%w: count needs warning or critical", alert.ErrInvalidRule)
	case r.Warning != 0 && r.Critical != 0 && r.Critical <= r.Warning:
		return fmt.Errorf("%w: critical must count more than warning", alert.ErrInvalidRule)
	case r.Count != CountEvents && r.Count != CountNodes:
		return fmt.Errorf("%w: count of events or nodes", alert.ErrInvalidRule)
	case r.Count == CountNodes && len(r.GroupBy) == 0:
		return fmt.Errorf("%w: counting nodes needs groupBy (the same IP on several nodes)", alert.ErrInvalidRule)
	case r.Learning < 0 || r.Learning > maxLearning:
		return fmt.Errorf("%w: learning must be between 0 and %v", alert.ErrInvalidRule, maxLearning)
	}
	if err := validEventWindow("count.window", r.Window); err != nil {
		return err
	}
	if err := r.validateFields(); err != nil {
		return err
	}
	if a := r.After; a != nil {
		if _, err := secevent.ParseKind(string(a.Kind)); err != nil {
			return fmt.Errorf("%w: after.event %q is no security event", alert.ErrInvalidRule, a.Kind)
		}
		if a.Count < 1 || a.Count > maxEventCount {
			return fmt.Errorf("%w: after.count must be between 1 and %d", alert.ErrInvalidRule, maxEventCount)
		}
		if err := validEventWindow("after.window", a.Window); err != nil {
			return err
		}
		for _, name := range r.GroupBy {
			if !secevent.Groupable(a.Kind, name) {
				return fmt.Errorf("%w: after.event %s has no %s to compare", alert.ErrInvalidRule, a.Kind, name)
			}
		}
	}
	return nil
}

func (r EventRule) validateFields() error {
	fields := secevent.FieldsOf(r.Kind)
	for i, name := range r.GroupBy {
		if !secevent.Groupable(r.Kind, name) || slices.Contains(r.GroupBy[:i], name) {
			return fmt.Errorf("%w: groupBy takes ip and user once each, where %s has them; not %q", alert.ErrInvalidRule, r.Kind, name)
		}
	}
	for i, name := range r.NotifyFields {
		if !slices.Contains(fields, name) || slices.Contains(r.NotifyFields[:i], name) {
			return fmt.Errorf("%w: notifyFields takes each of %v once, not %q", alert.ErrInvalidRule, fields, name)
		}
	}
	for name, value := range r.Match {
		if name == "ts" || !slices.Contains(fields, name) {
			return fmt.Errorf("%w: match takes the fields %v (not ts), not %q", alert.ErrInvalidRule, fields, name)
		}
		if value == "" || len(value) > maxMatchValue {
			return fmt.Errorf("%w: match.%s needs a value of 1 to %d bytes", alert.ErrInvalidRule, name, maxMatchValue)
		}
	}
	return nil
}

func (r EventRule) thresholds() alert.Thresholds {
	var th alert.Thresholds
	if r.Warning > 0 {
		th.Warning = &alert.Condition{Op: alert.AboveOrEqual, Value: float64(r.Warning)}
	}
	if r.Critical > 0 {
		th.Critical = &alert.Condition{Op: alert.AboveOrEqual, Value: float64(r.Critical)}
	}
	return th
}

// read returns the events of a kind received since since; truncated if there were more.
func read(ctx context.Context, env Env, kind secevent.Kind, since time.Time) ([]secevent.Event, bool, error) {
	ids := make([]identity.NodeID, len(env.Nodes))
	for i, n := range env.Nodes {
		ids[i] = n.ID
	}
	events, err := env.Events.Received(ctx, env.Scope, EventQuery{Kind: kind, Nodes: ids, Since: since, Limit: MaxEventsRead + 1})
	if err != nil {
		return nil, false, fmt.Errorf("read %s events: %w", kind, err)
	}
	if len(events) > MaxEventsRead {
		return events[:MaxEventsRead], true, nil
	}
	return events, false, nil
}

// group is the events of one instance (CountEvents) or of one groupBy key across nodes.
type group struct {
	node      identity.NodeID // zero for CountNodes
	dimension string
	events    []secevent.Event
}

// Evaluate implements Evaluator.
func (r EventRule) Evaluate(ctx context.Context, env Env) ([]Result, error) {
	if env.Events == nil {
		return nil, fmt.Errorf("%w: no event reader", alert.ErrInvalidRule)
	}
	events, truncated, err := read(ctx, env, r.Kind, env.Now.Add(-r.Window))
	if err != nil {
		return nil, err
	}
	learned := r.learnedSince(env)
	events = slices.DeleteFunc(events, func(e secevent.Event) bool {
		return !r.matches(e) || e.OccurredAt.Before(learned[e.Node])
	})
	if r.After != nil && len(events) > 0 {
		var cut bool
		if events, cut, err = r.preceded(ctx, env, events); err != nil {
			return nil, err
		}
		truncated = truncated || cut
	}

	th := r.thresholds()
	var out []Result
	for _, g := range r.groups(events) {
		out = append(out, r.results(g, th)...)
	}
	slices.SortFunc(out, func(a, b Result) int {
		return cmp.Or(cmp.Compare(b.Observation.Value, a.Observation.Value), cmp.Compare(a.Node.String(), b.Node.String()), cmp.Compare(a.Dimension, b.Dimension))
	})
	if len(out) > MaxEventInstances {
		out, truncated = out[:MaxEventInstances], true
	}
	// Open instances without events: no events is a count of zero – unless the round saw only
	// part of them.
	seen := make(map[alert.Key]bool, len(out))
	for _, res := range out {
		seen[alert.Key{Node: res.Node, Dimension: res.Dimension}] = true
	}
	for _, k := range env.Open {
		if seen[alert.Key{Node: k.Node, Dimension: k.Dimension}] {
			continue
		}
		o := alert.Observation{Level: alert.LevelOK, Cleared: true}
		if truncated {
			o = alert.Observation{NoData: true}
		}
		out = append(out, Result{Node: k.Node, Dimension: k.Dimension, Observation: o})
	}
	return out, nil
}

// learnedSince is when the learning of each node ends; nil without learning. A node without
// a start of learning has none.
func (r EventRule) learnedSince(env Env) map[identity.NodeID]time.Time {
	if r.Learning == 0 {
		return nil
	}
	out := make(map[identity.NodeID]time.Time, len(env.Nodes))
	for _, n := range env.Nodes {
		if !n.LearningSince.IsZero() {
			out[n.ID] = n.LearningSince.Add(r.Learning)
		}
	}
	return out
}

func (r EventRule) matches(e secevent.Event) bool {
	for name, want := range r.Match {
		if v, ok := e.Field(name); !ok || v != want {
			return false
		}
	}
	return true
}

// dimension returns the groupBy key of an event, false if it lacks a value. The values are
// sanitized like notification fields: the dimension is shown as well.
func (r EventRule) dimension(e secevent.Event) (string, bool) {
	if len(r.GroupBy) == 0 {
		return "", true
	}
	labels := metrics.Labels{}
	for _, name := range r.GroupBy {
		v, ok := e.GroupKey(name)
		if !ok {
			return "", false
		}
		labels[name] = notifyValue(v)
	}
	return labels.Key(), true
}

// preceded keeps the events that follow at least After.Count events of After.Kind of the same
// node and group within After.Window.
func (r EventRule) preceded(ctx context.Context, env Env, events []secevent.Event) ([]secevent.Event, bool, error) {
	before, truncated, err := read(ctx, env, r.After.Kind, env.Now.Add(-r.Window-r.After.Window))
	if err != nil {
		return nil, false, err
	}
	times := map[string][]time.Time{}
	for _, b := range before {
		if dim, ok := r.dimension(b); ok {
			k := b.Node.String() + "|" + dim
			times[k] = append(times[k], b.OccurredAt)
		}
	}
	kept := events[:0]
	for _, e := range events {
		dim, ok := r.dimension(e)
		if !ok {
			continue
		}
		n := 0
		for _, t := range times[e.Node.String()+"|"+dim] {
			if !t.After(e.OccurredAt) && t.After(e.OccurredAt.Add(-r.After.Window)) {
				n++
			}
		}
		if n >= r.After.Count {
			kept = append(kept, e)
		}
	}
	return kept, truncated, nil
}

// groups splits the events into instances (CountEvents) or groupBy keys (CountNodes), each
// sorted by occurrence.
func (r EventRule) groups(events []secevent.Event) []group {
	byKey := map[string]*group{}
	var order []string
	for _, e := range events {
		dim, ok := r.dimension(e)
		if !ok {
			continue
		}
		g := group{dimension: dim}
		if r.Count == CountEvents {
			g.node = e.Node
		}
		k := g.node.String() + "|" + dim
		if byKey[k] == nil {
			byKey[k] = &g
			order = append(order, k)
		}
		byKey[k].events = append(byKey[k].events, e)
	}
	out := make([]group, 0, len(order))
	for _, k := range order {
		g := byKey[k]
		slices.SortStableFunc(g.events, func(a, b secevent.Event) int {
			return cmp.Or(a.OccurredAt.Compare(b.OccurredAt), cmp.Compare(a.ID, b.ID))
		})
		out = append(out, *g)
	}
	return out
}

func (r EventRule) results(g group, th alert.Thresholds) []Result {
	if r.Count == CountEvents {
		o := th.Evaluate(float64(maxInWindow(g.events, r.Window)))
		o.Fields = r.fields(g.events[len(g.events)-1], unverified(g.events))
		return []Result{{Node: g.node, Dimension: g.dimension, Observation: o}}
	}
	nodes := maxNodesInWindow(g.events, r.Window)
	latest := map[identity.NodeID]secevent.Event{}
	var order []identity.NodeID
	for _, e := range g.events {
		if _, ok := latest[e.Node]; !ok {
			order = append(order, e.Node)
		}
		latest[e.Node] = e
	}
	out := make([]Result, 0, len(order))
	for _, n := range order {
		o := th.Evaluate(float64(nodes))
		o.Fields = r.fields(latest[n], unverified(g.events))
		out = append(out, Result{Node: n, Dimension: g.dimension, Observation: o})
	}
	return out
}

// unverified reports whether a counted event comes from a source anyone could write to.
func unverified(events []secevent.Event) bool {
	return slices.ContainsFunc(events, func(e secevent.Event) bool { return e.Unverified })
}

func (r EventRule) fields(e secevent.Event, unverified bool) alert.Fields {
	if len(r.NotifyFields) == 0 && !unverified {
		return nil
	}
	f := make(alert.Fields, len(r.NotifyFields)+1)
	for _, name := range r.NotifyFields {
		if v, ok := e.Field(name); ok {
			f[name] = notifyValue(v)
		}
	}
	if unverified {
		f[secevent.FieldSource] = secevent.SourceUnverified
	}
	return f
}

// notifyValue makes an attacker's text safe to pass on: control characters (line breaks, escape
// sequences) become U+FFFD, and the value ends after MaxFieldBytes on a character boundary.
func notifyValue(v string) string {
	var b strings.Builder
	for _, r := range v {
		if textsafe.Invisible(r) { // control, bidi, zero width, separators (M2-28)
			r = utf8.RuneError
		}
		if b.Len()+utf8.RuneLen(r) > MaxFieldBytes {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

// maxInWindow is the most events (sorted by occurrence) within less than window of each other.
func maxInWindow(events []secevent.Event, window time.Duration) int {
	best, first := 0, 0
	for last := range events {
		for events[last].OccurredAt.Sub(events[first].OccurredAt) >= window {
			first++
		}
		best = max(best, last-first+1)
	}
	return best
}

// maxNodesInWindow is the most distinct nodes among events within less than window.
func maxNodesInWindow(events []secevent.Event, window time.Duration) int {
	inWindow := map[identity.NodeID]int{}
	best, first := 0, 0
	for last := range events {
		inWindow[events[last].Node]++
		for events[last].OccurredAt.Sub(events[first].OccurredAt) >= window {
			n := events[first].Node
			if inWindow[n]--; inWindow[n] == 0 {
				delete(inWindow, n)
			}
			first++
		}
		best = max(best, len(inWindow))
	}
	return best
}
