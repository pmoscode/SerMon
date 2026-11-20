// Package alerting runs the alert engine (KONZEPT §12.1, M2-02): every Interval each server
// instance tries to lease the organizations that have rules; the holder evaluates their rules
// in the organization's scope, feeds the results into the state machine of domain/alert and
// stores changed instances together with their history in one transaction. A stopped instance's
// organizations are taken over once its lease expires.
package alerting

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

const (
	// Interval is how often the rules are evaluated.
	Interval = 10 * time.Second
	// LeaseDuration is how long an instance holds an organization without renewing it.
	LeaseDuration = 3 * Interval
)

// Result is what an evaluation found for one instance of a rule.
type Result struct {
	Node        identity.NodeID
	Dimension   string // labels key of the dimension (mountpoint, interface); empty if none
	Observation alert.Observation
}

// Querier reads metrics within a scope (metrics.Reader).
type Querier interface {
	Query(ctx context.Context, scope tenant.Scope, q metrics.Query) ([]metrics.Series, error)
}

// Env is what an evaluator sees: the organization's scope, its nodes, the time and the metrics.
type Env struct {
	Scope   tenant.Scope
	Nodes   []fleet.Node
	Now     time.Time
	Metrics Querier
}

// Evaluator evaluates one rule for the nodes of an organization (the rule types, M2-03).
type Evaluator interface {
	Evaluate(ctx context.Context, env Env) ([]Result, error)
}

// Rule is a rule ready for evaluation.
type Rule struct {
	ID        alert.RuleID
	Timing    alert.Timing
	Evaluator Evaluator
}

// RuleSource returns the rules of an organization (defaults with its overrides, M2-04).
type RuleSource interface {
	Rules(ctx context.Context, org tenant.OrgID) ([]Rule, error)
}

// NoRules is the rule source while there are no default rules yet (M2-04).
type NoRules struct{}

// Rules implements RuleSource.
func (NoRules) Rules(context.Context, tenant.OrgID) ([]Rule, error) { return nil, nil }

// Record is one transition of an instance, for the history and the notifications.
type Record struct {
	Key        alert.Key
	Transition alert.Transition
}

// Store persists instances and history; Instances and Save run in the organization's scope.
type Store interface {
	// Organizations returns all organizations (instance-level).
	Organizations(ctx context.Context) ([]tenant.OrgID, error)
	// Lease gives holder the organization until until, if nobody else holds it at now.
	Lease(ctx context.Context, org tenant.OrgID, holder string, now, until time.Time) (bool, error)
	Instances(ctx context.Context, scope tenant.Scope) ([]alert.Instance, error)
	// Save writes changed instances, deletes removed ones and appends the history – together.
	Save(ctx context.Context, scope tenant.Scope, changed []alert.Instance, removed []alert.Key, history []Record) error
}

// Sink receives the stored transitions of an organization (routing and notifiers, M2-14).
type Sink interface {
	Deliver(ctx context.Context, org tenant.OrgID, records []Record)
}

// Stats are counters for the server's own metrics.
type Stats struct {
	Rounds, Evaluations, Transitions, RuleErrors, OrgErrors atomic.Uint64
}

// Deps are the engine's collaborators. Holder identifies this server instance (random if empty).
type Deps struct {
	Store   Store
	Rules   RuleSource
	Nodes   fleet.Directory
	Metrics Querier
	Sink    Sink
	Now     func() time.Time
	Holder  string
	Logger  *slog.Logger
}

// Engine evaluates the rules of the organizations it holds.
type Engine struct {
	d     Deps
	stats Stats
}

// NewEngine returns an engine.
func NewEngine(d Deps) *Engine {
	if d.Holder == "" {
		raw := make([]byte, 8)
		_, _ = rand.Read(raw)
		d.Holder = hex.EncodeToString(raw)
	}
	return &Engine{d: d}
}

// Stats returns the counters.
func (e *Engine) Stats() *Stats { return &e.stats }

// Run evaluates every Interval until ctx ends.
func (e *Engine) Run(ctx context.Context) error {
	ticker := time.NewTicker(Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			e.Round(ctx)
		}
	}
}

// Round evaluates once every organization this instance holds or can lease.
func (e *Engine) Round(ctx context.Context) {
	e.stats.Rounds.Add(1)
	orgs, err := e.d.Store.Organizations(ctx)
	if err != nil {
		e.stats.OrgErrors.Add(1)
		e.d.Logger.ErrorContext(ctx, "alerting: list organizations failed", "error", err)
		return
	}
	for _, org := range orgs {
		if err := e.evaluate(ctx, org); err != nil {
			e.stats.OrgErrors.Add(1)
			e.d.Logger.ErrorContext(ctx, "alerting: evaluation failed", "org", org.String(), "error", err)
		}
	}
}

func (e *Engine) evaluate(ctx context.Context, org tenant.OrgID) error {
	rules, err := e.d.Rules.Rules(ctx, org)
	if err != nil || len(rules) == 0 {
		return err //nolint:wrapcheck // logged with the organization
	}
	now := e.d.Now()
	held, err := e.d.Store.Lease(ctx, org, e.d.Holder, now, now.Add(LeaseDuration))
	if err != nil || !held {
		return err //nolint:wrapcheck // logged with the organization
	}
	scope, err := tenant.ForAllGroups(org)
	if err != nil {
		return fmt.Errorf("scope: %w", err)
	}
	nodes, err := e.d.Nodes.List(ctx, scope)
	if err != nil {
		return fmt.Errorf("list nodes: %w", err)
	}
	loaded, err := e.d.Store.Instances(ctx, scope)
	if err != nil {
		return fmt.Errorf("load instances: %w", err)
	}
	instances := make(map[alert.Key]*alert.Instance, len(loaded))
	for i := range loaded {
		if msg := loaded[i].Check(); msg != "" {
			e.d.Logger.WarnContext(ctx, "alerting: inconsistent instance reset", "org", org.String(), "rule", string(loaded[i].Key.Rule), "problem", msg)
			continue
		}
		instances[loaded[i].Key] = &loaded[i]
	}

	env := Env{Scope: scope, Nodes: nodes, Now: now, Metrics: e.d.Metrics}
	var changed []alert.Instance
	var removed []alert.Key
	var history []Record
	for _, rule := range rules {
		results, err := rule.Evaluator.Evaluate(ctx, env)
		if err != nil {
			// One broken rule must not silence the others.
			e.stats.RuleErrors.Add(1)
			e.d.Logger.WarnContext(ctx, "alerting: rule evaluation failed", "org", org.String(), "rule", string(rule.ID), "error", err)
			continue
		}
		e.stats.Evaluations.Add(1)
		for _, r := range results {
			key := alert.Key{Rule: rule.ID, Node: r.Node, Dimension: r.Dimension}
			inst, existed := instances[key]
			if !existed {
				inst = alert.NewInstance(key, now)
			}
			transitions := inst.Observe(r.Observation, now, rule.Timing)
			for _, t := range transitions {
				history = append(history, Record{Key: key, Transition: t})
			}
			switch quiet := inst.State == alert.StateOK && len(inst.Flips) == 0 && !inst.Flapping; {
			case quiet && existed:
				removed = append(removed, key)
			case !quiet && (len(transitions) > 0 || !existed):
				changed = append(changed, *inst)
			}
		}
	}
	if len(changed) == 0 && len(removed) == 0 && len(history) == 0 {
		return nil
	}
	if err := e.d.Store.Save(ctx, scope, changed, removed, history); err != nil {
		return fmt.Errorf("save: %w", err)
	}
	e.stats.Transitions.Add(uint64(len(history)))
	if e.d.Sink != nil && len(history) > 0 {
		e.d.Sink.Deliver(ctx, org, history)
	}
	return nil
}
