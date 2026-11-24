// Package alerts holds the use cases of the alert page (KONZEPT §12, M2-16): the active alerts of
// the caller's groups with names, units and what mutes them, the history of an instance and
// acknowledging, which the routing then reports to the channels.
package alerts

import (
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/alerting"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/app/routing"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	"github.com/pmoscode/SerMon/server/internal/secrets"
)

// ErrNotFiring means the alert does not fire (anymore) or is acknowledged already.
var ErrNotFiring = errors.New("the alert does not fire or is acknowledged already")

// ErrInvalid means a request the service doesn't take.
var ErrInvalid = errors.New("invalid request")

// MaxHistory bounds the history of an instance in one answer.
const MaxHistory = 100

// Store reads the instances and their history and acknowledges (postgres.AlertStore).
type Store interface {
	Instances(ctx context.Context, scope tenant.Scope) ([]alert.Instance, error)
	History(ctx context.Context, scope tenant.Scope, key alert.Key, limit int32) ([]alerting.Record, error)
	// Acknowledge returns alert.ErrNotFiring if the alert stopped firing or is acknowledged.
	Acknowledge(ctx context.Context, scope tenant.Scope, key alert.Key, ack alert.Ack, rec alerting.Record) error
	// HistorySince returns the transitions at or after since, oldest first, at most limit and
	// without their fields (the statistics, M3-19).
	HistorySince(ctx context.Context, scope tenant.Scope, since time.Time, limit int) ([]alerting.Record, error)
}

// Mutes lists what mutes alerts (postgres.RoutingStore).
type Mutes interface {
	Silences(ctx context.Context, scope tenant.Scope, since time.Time) ([]routing.Silence, error)
	Windows(ctx context.Context, scope tenant.Scope) ([]routing.MaintenanceWindow, error)
}

// Deps are the collaborators of Service. Sink is the router: it tells the channels about an
// acknowledgement.
type Deps struct {
	Store  Store
	Nodes  fleet.Directory
	Rules  routing.Rules
	Users  routing.Users
	Mutes  Mutes
	Sink   alerting.Sink
	Policy authz.Policy
	Now    func() time.Time
	// Box seals the dimensions of event rules for callers who may not see security events
	// (M2-28); without it they are hidden and can't be addressed.
	Box *secrets.Box
}

// Service serves the alert page.
type Service struct{ d Deps }

// NewService returns the service.
func NewService(d Deps) *Service { return &Service{d: d} }

// Alert is an active instance as the page shows it. Fields are set only for callers who may see
// the security events of the node's group (user names, addresses).
type Alert struct {
	alert.Instance
	RuleName       notify.Text
	Labels         map[string]string
	Unit           notify.Unit
	Hostname       string
	Group          tenant.GroupID
	GroupName      string
	AckByName      string
	Muted          bool
	CanAcknowledge bool
}

// Entry is a transition of the history, with who acknowledged.
type Entry struct {
	alerting.Record
	ByName string
}

// hiddenPrefix starts a sealed dimension: the address or user of an event rule, for a caller who
// may not see the security events of the node's group (M2-28).
const hiddenPrefix = "~"

// Hidden reports whether a dimension is sealed.
func Hidden(dimension string) bool { return strings.HasPrefix(dimension, hiddenPrefix) }

func sealContext(org tenant.OrgID, key alert.Key) []byte {
	return []byte("alert_dimension:" + org.String() + ":" + string(key.Rule) + ":" + key.Node.String())
}

// seal replaces the dimension by a sealed one, bound to organization, rule and node.
func (s *Service) seal(org tenant.OrgID, key alert.Key) alert.Key {
	if key.Dimension == "" {
		return key
	}
	sealed := []byte{}
	if s.d.Box != nil {
		if out, err := s.d.Box.Seal([]byte(key.Dimension), sealContext(org, key)); err == nil {
			sealed = out
		}
	}
	key.Dimension = hiddenPrefix + base64.RawURLEncoding.EncodeToString(sealed)
	return key
}

// unseal returns the key with its real dimension; a sealed one that doesn't open names nothing.
func (s *Service) unseal(org tenant.OrgID, key alert.Key) (alert.Key, error) {
	if !Hidden(key.Dimension) {
		return key, nil
	}
	sealed, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(key.Dimension, hiddenPrefix))
	if err != nil || s.d.Box == nil {
		return alert.Key{}, access.ErrNotFound
	}
	plain, err := s.d.Box.Open(sealed, sealContext(org, key))
	if err != nil {
		return alert.Key{}, access.ErrNotFound
	}
	key.Dimension = string(plain)
	return key, nil
}

// security reports whether acknowledging the rule's alerts needs a step-up: event rules (also an
// organization's own), the security category and the built-in security rules.
func security(rule alert.RuleID, info routing.RuleInfo) bool {
	return info.Event || info.Labels["category"] == "security" || strings.HasPrefix(string(rule), "security.")
}

// view is what a caller may do where.
type view struct {
	nodes  map[identity.NodeID]fleet.Node
	events tenant.Scope
	ack    tenant.Scope
}

func (s *Service) view(ctx context.Context, p authz.Principal) (view, error) {
	scope, err := s.d.Policy.ScopeForAction(p, authz.ViewNodes)
	if err != nil {
		return view{}, access.ErrForbidden
	}
	list, err := s.d.Nodes.List(ctx, scope)
	if err != nil {
		return view{}, fmt.Errorf("list nodes: %w", err)
	}
	v := view{nodes: make(map[identity.NodeID]fleet.Node, len(list))}
	for _, n := range list {
		v.nodes[n.ID] = n
	}
	// Without the right the scopes stay empty: nothing allowed.
	v.events, _ = s.d.Policy.ScopeForAction(p, authz.ViewSecurityEvents)
	v.ack, _ = s.d.Policy.ScopeForAction(p, authz.AcknowledgeAlerts)
	return v, nil
}

func orgScope(org tenant.OrgID) (tenant.Scope, error) {
	scope, err := tenant.ForAllGroups(org)
	if err != nil {
		return tenant.Scope{}, access.ErrForbidden
	}
	return scope, nil
}

func severityOf(s alert.State) notify.Severity {
	if s == alert.StateCritical {
		return notify.Critical
	}
	return notify.Warning
}

// urgency orders the list: critical, warning, pending, resolved; acknowledged ones after the
// others of their state.
func urgency(a Alert) int {
	u := map[alert.State]int{alert.StateCritical: 0, alert.StateWarning: 2, alert.StatePending: 4, alert.StateResolved: 5}[a.State]
	if a.Ack != nil {
		u++
	}
	return u
}

// List returns the active alerts (pending, firing, resolved) of the nodes the caller sees, most
// urgent first, newest first within.
func (s *Service) List(ctx context.Context, c access.Caller) ([]Alert, error) {
	v, err := s.view(ctx, c.Principal)
	if err != nil {
		return nil, err
	}
	org, err := orgScope(c.Principal.Org)
	if err != nil {
		return nil, err
	}
	instances, err := s.d.Store.Instances(ctx, org)
	if err != nil {
		return nil, fmt.Errorf("list alerts: %w", err)
	}
	rules, err := s.d.Rules.RuleInfos(ctx, c.Principal.Org)
	if err != nil {
		return nil, fmt.Errorf("rules: %w", err)
	}
	mute, err := s.muter(ctx, org)
	if err != nil {
		return nil, err
	}
	names, err := s.names(ctx, org, instances)
	if err != nil {
		return nil, err
	}
	out := make([]Alert, 0, len(instances))
	for _, i := range instances {
		node, ok := v.nodes[i.Key.Node]
		if !ok || i.State == alert.StateOK {
			continue
		}
		info, known := rules[i.Key.Rule]
		if !known {
			info.Name = notify.Text{De: string(i.Key.Rule), En: string(i.Key.Rule)}
		}
		a := Alert{
			Instance: i, RuleName: info.Name, Labels: info.Labels, Unit: info.Unit,
			Hostname: node.Hostname, Group: node.Group, GroupName: node.GroupName,
			CanAcknowledge: i.State.Firing() && i.Ack == nil && v.ack.AllowsGroup(node.Group),
			Muted:          mute(routing.Target{Rule: i.Key.Rule, Labels: info.Labels, Group: node.Group, Node: i.Key.Node, Severity: severityOf(i.State)}),
		}
		if i.Ack != nil {
			a.AckByName = names[i.Ack.By]
		}
		if !v.events.AllowsGroup(node.Group) {
			a.Fields = nil
			if info.Event || !known {
				a.Key = s.seal(c.Principal.Org, a.Key)
			}
		}
		out = append(out, a)
	}
	slices.SortStableFunc(out, func(x, y Alert) int {
		if c := cmp.Compare(urgency(x), urgency(y)); c != 0 {
			return c
		}
		return y.Since.Compare(x.Since)
	})
	return out, nil
}

// muter reports whether an active silence or maintenance window mutes a target now.
func (s *Service) muter(ctx context.Context, org tenant.Scope) (func(routing.Target) bool, error) {
	now := s.d.Now()
	silences, err := s.d.Mutes.Silences(ctx, org, now)
	if err != nil {
		return nil, fmt.Errorf("list silences: %w", err)
	}
	windows, err := s.d.Mutes.Windows(ctx, org)
	if err != nil {
		return nil, fmt.Errorf("list maintenance windows: %w", err)
	}
	return func(t routing.Target) bool {
		return slices.ContainsFunc(silences, func(x routing.Silence) bool { return x.ActiveAt(now) && x.Mutes(t) }) ||
			slices.ContainsFunc(windows, func(w routing.MaintenanceWindow) bool { return w.Mutes(t, now) })
	}, nil
}

func (s *Service) names(ctx context.Context, org tenant.Scope, instances []alert.Instance) (map[identity.UserID]string, error) {
	if !slices.ContainsFunc(instances, func(i alert.Instance) bool { return i.Ack != nil }) {
		return nil, nil
	}
	names, err := s.d.Users.DisplayNames(ctx, org)
	if err != nil {
		return nil, fmt.Errorf("display names: %w", err)
	}
	return names, nil
}

// History returns the newest transitions of an instance of a node the caller sees.
func (s *Service) History(ctx context.Context, c access.Caller, key alert.Key) ([]Entry, error) {
	v, err := s.view(ctx, c.Principal)
	if err != nil {
		return nil, err
	}
	if key, err = s.unseal(c.Principal.Org, key); err != nil {
		return nil, err
	}
	node, ok := v.nodes[key.Node]
	if !ok {
		return nil, access.ErrNotFound
	}
	org, err := orgScope(c.Principal.Org)
	if err != nil {
		return nil, err
	}
	records, err := s.d.Store.History(ctx, org, key, MaxHistory)
	if err != nil {
		return nil, fmt.Errorf("alert history: %w", err)
	}
	var names map[identity.UserID]string
	if slices.ContainsFunc(records, func(r alerting.Record) bool { return !r.Transition.By.IsZero() }) {
		if names, err = s.d.Users.DisplayNames(ctx, org); err != nil {
			return nil, fmt.Errorf("display names: %w", err)
		}
	}
	fields := v.events.AllowsGroup(node.Group)
	out := make([]Entry, len(records))
	for i, r := range records {
		if !fields {
			r.Transition.Fields = nil
		}
		out[i] = Entry{Record: r, ByName: names[r.Transition.By]}
	}
	return out, nil
}

// Acknowledge marks a firing alert as seen: its reminders and escalation stop until it ends or
// becomes critical, and the channels hear who acknowledged it. Operators of the node's group
// may; security alerts need a fresh step-up.
func (s *Service) Acknowledge(ctx context.Context, c access.Caller, key alert.Key) error {
	v, err := s.view(ctx, c.Principal)
	if err != nil {
		return err
	}
	node, ok := v.nodes[key.Node]
	if !ok {
		return access.ErrNotFound
	}
	if !v.ack.AllowsGroup(node.Group) {
		return access.ErrForbidden
	}
	if key, err = s.unseal(c.Principal.Org, key); err != nil {
		return err
	}
	rules, err := s.d.Rules.RuleInfos(ctx, c.Principal.Org)
	if err != nil {
		return fmt.Errorf("rules: %w", err)
	}
	now := s.d.Now()
	if security(key.Rule, rules[key.Rule]) && now.Sub(c.StepUpAt) > access.StepUpMaxAge {
		return access.ErrStepUpRequired
	}
	org, err := orgScope(c.Principal.Org)
	if err != nil {
		return err
	}
	instances, err := s.d.Store.Instances(ctx, org)
	if err != nil {
		return fmt.Errorf("list alerts: %w", err)
	}
	i := slices.IndexFunc(instances, func(x alert.Instance) bool { return x.Key == key })
	if i < 0 {
		return access.ErrNotFound
	}
	t, err := instances[i].Acknowledge(c.Principal.User, now)
	if errors.Is(err, alert.ErrNotFiring) {
		return ErrNotFiring
	} else if err != nil {
		return fmt.Errorf("acknowledge: %w", err)
	}
	rec := alerting.Record{Key: key, Transition: t}
	if err := s.d.Store.Acknowledge(ctx, org, key, *instances[i].Ack, rec); errors.Is(err, alert.ErrNotFiring) {
		return ErrNotFiring
	} else if err != nil {
		return fmt.Errorf("acknowledge: %w", err)
	}
	all, err := s.d.Nodes.List(ctx, org)
	if err != nil {
		return fmt.Errorf("list nodes: %w", err)
	}
	// The change alone: the engine's rounds handle reminders and clean-up.
	s.d.Sink.Deliver(ctx, alerting.Round{Org: c.Principal.Org, Now: now, Records: []alerting.Record{rec}, Instances: instances, Nodes: all, Partial: true})
	return nil
}

// MaxStatisticsRecords bounds the history one statistics reads (M3-19); a busier range is
// evaluated on its oldest part and marked truncated.
const MaxStatisticsRecords = 200_000

// StatisticsDays are the ranges of the statistics.
var StatisticsDays = []int{7, 30}

// RuleStatistics are the figures of a rule with its name and labels.
type RuleStatistics struct {
	RuleFigures
	Name   notify.Text
	Labels map[string]string
}

// StatisticsResult is what the alert page shows.
type StatisticsResult struct {
	Statistics
	Rules []RuleStatistics
}

// Statistics returns the loudest rules and the times to acknowledgement and resolution of the
// last days (7 or 30) on the nodes the caller sees (M3-19).
func (s *Service) Statistics(ctx context.Context, c access.Caller, days int) (StatisticsResult, error) {
	if !slices.Contains(StatisticsDays, days) {
		return StatisticsResult{}, fmt.Errorf("%w: days must be one of %v", ErrInvalid, StatisticsDays)
	}
	v, err := s.view(ctx, c.Principal)
	if err != nil {
		return StatisticsResult{}, err
	}
	org, err := orgScope(c.Principal.Org)
	if err != nil {
		return StatisticsResult{}, err
	}
	now := s.d.Now()
	from := now.AddDate(0, 0, -days)
	records, err := s.d.Store.HistorySince(ctx, org, from, MaxStatisticsRecords+1)
	if err != nil {
		return StatisticsResult{}, fmt.Errorf("alert statistics: %w", err)
	}
	truncated := len(records) > MaxStatisticsRecords
	if truncated {
		records = records[:MaxStatisticsRecords]
	}
	records = slices.DeleteFunc(records, func(r alerting.Record) bool {
		_, ok := v.nodes[r.Key.Node]
		return !ok
	})
	stats := ComputeStatistics(records, from, now)
	stats.Truncated = truncated
	rules, err := s.d.Rules.RuleInfos(ctx, c.Principal.Org)
	if err != nil {
		return StatisticsResult{}, fmt.Errorf("rules: %w", err)
	}
	out := StatisticsResult{Statistics: stats, Rules: make([]RuleStatistics, len(stats.Rules))}
	for i, r := range stats.Rules {
		info, known := rules[r.Rule]
		if !known {
			info.Name = notify.Text{De: string(r.Rule), En: string(r.Rule)}
		}
		out.Rules[i] = RuleStatistics{RuleFigures: r, Name: info.Name, Labels: info.Labels}
	}
	return out, nil
}
