package routing

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync/atomic"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/alerting"
	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// RuleInfo is what notifications and matchers need of a rule.
type RuleInfo struct {
	Name   notify.Text
	Labels map[string]string
	Unit   notify.Unit
}

// Rules returns the rules of an organization, disabled ones included (their alerts resolve).
type Rules interface {
	RuleInfos(ctx context.Context, org tenant.OrgID) (map[alert.RuleID]RuleInfo, error)
}

// Users names the members of an organization (who acknowledged).
type Users interface {
	DisplayNames(ctx context.Context, scope tenant.Scope) (map[identity.UserID]string, error)
}

// Snapshot is what the router reads per round: all policies in order (disabled ones included),
// the enabled channels, the states, the silences that have not ended and the enabled maintenance
// windows.
type Snapshot struct {
	Policies []Policy
	Channels []notify.ChannelID
	States   []State
	Silences []Silence
	Windows  []MaintenanceWindow
}

// Store reads the routing of an organization and writes a round's outcome in one transaction:
// the deliveries are queued together with the states, so a reminder is never queued twice.
type Store interface {
	Snapshot(ctx context.Context, scope tenant.Scope, now time.Time) (Snapshot, error)
	Commit(ctx context.Context, scope tenant.Scope, deliveries []notify.Delivery, save []State, del []StateKey) error
}

// RouterDeps are the router's collaborators.
type RouterDeps struct {
	Store  Store
	Rules  Rules
	Users  Users
	Logger *slog.Logger
}

// Stats are counters for the server's own metrics.
type Stats struct {
	Deliveries, Errors atomic.Uint64
}

// Router is the engine's Sink: it routes each round's changes and timers to the channels.
type Router struct {
	d     RouterDeps
	stats Stats
}

var _ alerting.Sink = (*Router)(nil)

// NewRouter returns a router.
func NewRouter(d RouterDeps) *Router { return &Router{d: d} }

// Stats returns the counters.
func (r *Router) Stats() *Stats { return &r.stats }

// Deliver implements alerting.Sink. Errors are logged and counted: the alerts are stored, and the
// next round repeats what is due.
func (r *Router) Deliver(ctx context.Context, round alerting.Round) {
	if err := r.deliver(ctx, round); err != nil {
		r.stats.Errors.Add(1)
		r.d.Logger.ErrorContext(ctx, "routing: round failed", "org", round.Org.String(), "error", err)
	}
}

// busy reports whether the round can notify at all: changes, or something firing that nobody
// acknowledged (repeats, escalations).
func busy(round alerting.Round) bool {
	return len(round.Records) > 0 || slices.ContainsFunc(round.Instances, func(i alert.Instance) bool {
		return i.State.Firing() && i.Ack == nil
	})
}

func (r *Router) deliver(ctx context.Context, round alerting.Round) error {
	if !busy(round) {
		return nil
	}
	scope, err := tenant.ForAllGroups(round.Org)
	if err != nil {
		return fmt.Errorf("scope: %w", err)
	}
	snap, err := r.d.Store.Snapshot(ctx, scope, round.Now)
	if err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	rules, err := r.d.Rules.RuleInfos(ctx, round.Org)
	if err != nil {
		return fmt.Errorf("rules: %w", err)
	}
	nodes := make(map[identity.NodeID]fleet.Node, len(round.Nodes))
	for _, n := range round.Nodes {
		nodes[n.ID] = n
	}
	in := Input{
		Now: round.Now, Records: round.Records, Instances: round.Instances, States: snap.States,
		Silences: snap.Silences, Windows: snap.Windows, ChangesOnly: round.Partial,
		Target: func(k alert.Key) Target {
			return Target{Rule: k.Rule, Labels: rules[k.Rule].Labels, Group: nodes[k.Node].Group}
		},
	}
	for _, p := range snap.Policies {
		if p.Enabled {
			in.Policies = append(in.Policies, p)
		}
	}
	if len(snap.Policies) == 0 {
		in.Fallback = snap.Channels
	}
	out := Plan(in)
	enabled := map[notify.ChannelID]bool{}
	for _, c := range snap.Channels {
		enabled[c] = true
	}
	out.Sends = slices.DeleteFunc(out.Sends, func(s Send) bool { return !enabled[s.Channel] })
	if len(out.Sends) == 0 && len(out.Save) == 0 && len(out.Delete) == 0 {
		return nil
	}
	names, err := r.names(ctx, scope, out.Sends)
	if err != nil {
		return err
	}
	build := func(e Event) notify.Notification {
		info, ok := rules[e.Key.Rule]
		if !ok {
			info.Name = notify.Text{De: string(e.Key.Rule), En: string(e.Key.Rule)}
		}
		return notify.Notification{
			Kind: e.Kind, Severity: e.Severity, Rule: e.Key.Rule, RuleName: info.Name, Node: e.Key.Node,
			Hostname: nodes[e.Key.Node].Hostname, Dimension: e.Key.Dimension, Value: e.Value, Unit: info.Unit,
			Fields: e.Fields, At: e.At, AckBy: names[e.By], Reason: e.Reason, FiringSeconds: int64(e.FiringFor.Seconds()),
		}
	}
	deliveries := deliveriesOf(out.Sends, build, round)
	if err := r.d.Store.Commit(ctx, scope, deliveries, out.Save, out.Delete); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	r.stats.Deliveries.Add(uint64(len(deliveries)))
	return nil
}

func (r *Router) names(ctx context.Context, scope tenant.Scope, sends []Send) (map[identity.UserID]string, error) {
	if !slices.ContainsFunc(sends, func(s Send) bool { return s.Event.Kind == notify.Acknowledged }) {
		return nil, nil
	}
	names, err := r.d.Users.DisplayNames(ctx, scope)
	if err != nil {
		return nil, fmt.Errorf("display names: %w", err)
	}
	return names, nil
}

type groupKey struct {
	channel notify.ChannelID
	node    identity.NodeID // zero for a digest of all nodes
}

// deliveriesOf groups the sends per channel (and node, if the policy groups so), in the order
// the groups begin; a group of several becomes a digest.
func deliveriesOf(sends []Send, build func(Event) notify.Notification, round alerting.Round) []notify.Delivery {
	var order []groupKey
	groups := map[groupKey][]notify.Notification{}
	for _, s := range sends {
		k := groupKey{channel: s.Channel}
		if s.Grouping == GroupByNode {
			k.node = s.Event.Key.Node
		}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], build(s.Event))
	}
	out := make([]notify.Delivery, 0, len(order))
	for _, k := range order {
		n := groups[k][0]
		if len(groups[k]) > 1 {
			n = notify.Digest(round.Now, groups[k], 0)
		}
		out = append(out, notify.Delivery{Channel: k.channel, Notification: n, Status: notify.StatusPending, CreatedAt: round.Now, NextAttemptAt: round.Now})
	}
	return out
}
