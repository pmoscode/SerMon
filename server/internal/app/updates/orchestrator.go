package updates

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/domain/command"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// ErrLeaseLost means another instance took the organization's lease during a round: nothing of it
// was saved.
var ErrLeaseLost = errors.New("update lease lost")

// GroupState is a group with an installing policy: its last rollout and its nodes.
type GroupState struct {
	Policy  Policy
	Rollout *RolloutRun
	Nodes   []NodeView
}

// OrgState is what the orchestrator reads of an organization in a round.
type OrgState struct {
	Groups []GroupState
	// Runs are the commands of the active nodes of the rollouts.
	Runs map[uuid.UUID]command.Command
}

// Change is a rollout to save with the actions of its step.
type Change struct {
	Rollout RolloutRun
	Actions []Action
}

// RolloutStore is the orchestrator's port to the database.
type RolloutStore interface {
	// Organizations returns the organizations to look after (not locked).
	Organizations(ctx context.Context) ([]tenant.OrgID, error)
	// Lease takes or renews the organization's lease for leaseFor from the database's time; false
	// if another holds it.
	Lease(ctx context.Context, org tenant.OrgID, holder string, leaseFor time.Duration) (bool, error)
	// Load reads the groups with an installing policy (and those with a rollout still followed).
	Load(ctx context.Context, org tenant.OrgID, now time.Time) (OrgState, error)
	// Save stores the changes in one transaction while holder holds the lease (else
	// ErrLeaseLost): rollouts, commands (with their wake-up), silences and server events. It drops
	// the change of a rollout cancelled since the load, and while the updates are paused every
	// change that sends a command (M4-31).
	Save(ctx context.Context, org tenant.OrgID, holder string, now time.Time, changes []Change) error
	// Prune deletes the organization's runs and rollouts created before before.
	Prune(ctx context.Context, org tenant.OrgID, before time.Time) error
}

// OrchestratorConfig are the orchestrator's timing and limits.
type OrchestratorConfig struct {
	Holder        string
	Interval      time.Duration
	LeaseFor      time.Duration
	OrgLimit      int // runs at once per organization
	InstanceLimit int // runs at once on this instance, all organizations
	Retention     time.Duration
	// Releases are the agent releases for the groups that update their agents (nil: none).
	Releases Releases
	Now      func() time.Time
	NewID    func() uuid.UUID
	Logger   *slog.Logger
}

// DefaultOrchestratorConfig returns the defaults for holder.
func DefaultOrchestratorConfig(holder string, logger *slog.Logger) OrchestratorConfig {
	return OrchestratorConfig{
		Holder: holder, Interval: 15 * time.Second, LeaseFor: time.Minute, OrgLimit: 100, InstanceLimit: 500,
		Retention: 365 * 24 * time.Hour, Now: time.Now, Logger: logger,
		NewID: func() uuid.UUID { return uuid.Must(uuid.NewV7()) },
	}
}

// OrchestratorStats count the orchestrator's work.
type OrchestratorStats struct {
	Rounds, Started, Failed, Stopped, LeasesLost, Errors atomic.Uint64
}

// Orchestrator rolls the update policies out (KONZEPT §14.2, M4-13): one instance per
// organization under a lease, a step of each group's rollout per round.
type Orchestrator struct {
	store  RolloutStore
	config OrchestratorConfig
	stats  OrchestratorStats
	// active are the runs per organization at the end of the last round; turn rotates the order
	// of the organizations, so none always comes first to the instance's room.
	active map[tenant.OrgID]int
	turn   int
	pruned map[tenant.OrgID]time.Time
}

// NewOrchestrator returns the orchestrator.
func NewOrchestrator(store RolloutStore, config OrchestratorConfig) *Orchestrator {
	return &Orchestrator{store: store, config: config, active: map[tenant.OrgID]int{}, pruned: map[tenant.OrgID]time.Time{}}
}

// Stats returns the counters.
func (o *Orchestrator) Stats() *OrchestratorStats { return &o.stats }

// Run runs a round every interval until ctx ends.
func (o *Orchestrator) Run(ctx context.Context) error {
	tick := time.NewTicker(o.config.Interval)
	defer tick.Stop()
	for {
		o.Round(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// Round steps the rollouts of every organization whose lease this instance holds.
func (o *Orchestrator) Round(ctx context.Context) {
	o.stats.Rounds.Add(1)
	orgs, err := o.store.Organizations(ctx)
	if err != nil {
		o.stats.Errors.Add(1)
		o.config.Logger.ErrorContext(ctx, "updates: list organizations failed", "error", err)
		return
	}
	// An organization gone (deleted or locked) frees its places of the instance (M4-31).
	for org := range o.active {
		if !slices.Contains(orgs, org) {
			delete(o.active, org)
		}
	}
	for org := range o.pruned {
		if !slices.Contains(orgs, org) {
			delete(o.pruned, org)
		}
	}
	if len(orgs) == 0 {
		return
	}
	o.turn = (o.turn + 1) % len(orgs)
	total := 0
	for _, n := range o.active {
		total += n
	}
	for i := range orgs {
		org := orgs[(o.turn+i)%len(orgs)]
		active, err := o.step(ctx, org, total)
		switch {
		case errors.Is(err, ErrLeaseLost):
			o.stats.LeasesLost.Add(1)
			o.config.Logger.WarnContext(ctx, "updates: lease lost during the round", "org", org.String())
		case err != nil:
			o.stats.Errors.Add(1)
			o.config.Logger.ErrorContext(ctx, "updates: round failed", "org", org.String(), "error", err)
		}
		total += active - o.active[org]
		if active == 0 {
			delete(o.active, org)
		} else {
			o.active[org] = active
		}
	}
}

// step plans the organization's groups; it returns its runs after the step.
func (o *Orchestrator) step(ctx context.Context, org tenant.OrgID, instanceActive int) (int, error) {
	now := o.config.Now()
	held, err := o.store.Lease(ctx, org, o.config.Holder, o.config.LeaseFor)
	if err != nil {
		return 0, fmt.Errorf("lease: %w", err)
	}
	if !held {
		return 0, nil
	}
	if last, ok := o.pruned[org]; !ok || now.Sub(last) > 24*time.Hour {
		if err := o.store.Prune(ctx, org, now.Add(-o.config.Retention)); err != nil {
			return 0, fmt.Errorf("prune: %w", err)
		}
		o.pruned[org] = now
	}
	state, err := o.store.Load(ctx, org, now)
	if err != nil {
		return 0, fmt.Errorf("load: %w", err)
	}
	if o.config.Releases != nil {
		for i, g := range state.Groups {
			if g.Policy.AgentUpdate != AgentAuto {
				continue
			}
			for j, n := range g.Nodes {
				latest, _ := o.config.Releases.Latest(g.Policy.Channel, n.Agent.Arch)
				state.Groups[i].Nodes[j].AgentTarget = n.Agent.Target(latest)
			}
		}
	}
	orgActive := 0
	for _, g := range state.Groups {
		if g.Rollout != nil {
			for _, n := range g.Rollout.Nodes {
				if n.State.Active() {
					orgActive++
				}
			}
		}
	}
	instanceOthers := instanceActive - o.active[org]
	room := Room(min(o.config.OrgLimit-orgActive, o.config.InstanceLimit-instanceOthers-orgActive))
	// maxParallel holds for the group, not per rollout: a rollout by hand during the policy's,
	// or two by hand, share it; a node active in one waits in the others (review M4-30).
	activeIn := map[identity.NodeID]uuid.UUID{}
	groupActive := map[tenant.GroupID]int{}
	for _, g := range state.Groups {
		if g.Rollout != nil {
			for _, n := range g.Rollout.Nodes {
				if n.State.Active() {
					activeIn[n.Node] = g.Rollout.ID
					groupActive[g.Rollout.Group]++
				}
			}
		}
	}
	var changes []Change
	after := 0
	for _, g := range state.Groups {
		var id uuid.UUID
		own := 0
		if g.Rollout != nil {
			id, own = g.Rollout.ID, activeOf(*g.Rollout)
		}
		nodes := slices.Clone(g.Nodes)
		for i := range nodes {
			if in, ok := activeIn[nodes[i].ID]; ok && in != id {
				nodes[i].Busy = true
			}
		}
		groupRoom := Room(g.Policy.Rollout.MaxParallel - (groupActive[g.Policy.Group] - own))
		r, actions := Plan(now, g.Policy, g.Rollout, nodes, state.Runs, min(room, groupRoom), o.config.NewID)
		if r == nil {
			continue
		}
		groupActive[g.Policy.Group] += activeOf(*r) - own
		// Every node that began a run takes a place, an agent update like packages (M4-31).
		room -= Room(max(activeOf(*r)-own, 0))
		for _, n := range r.Nodes {
			if n.State.Active() {
				activeIn[n.Node] = r.ID
			}
		}
		for _, a := range actions {
			switch {
			case a.Command != nil && (a.Command.Kind == command.Update || a.Command.Kind == command.SelfUpdate):
				o.stats.Started.Add(1)
			case a.Event == EventRunFailed:
				o.stats.Failed.Add(1)
			case a.Event == EventRolloutStopped:
				o.stats.Stopped.Add(1)
			}
		}
		for _, n := range r.Nodes {
			if n.State.Active() {
				after++
			}
		}
		if g.Rollout == nil || len(actions) > 0 || changed(*g.Rollout, *r) {
			changes = append(changes, Change{Rollout: *r, Actions: actions})
		}
	}
	if len(changes) == 0 {
		return after, nil
	}
	if err := o.store.Save(ctx, org, o.config.Holder, now, changes); err != nil {
		return orgActive, fmt.Errorf("save: %w", err)
	}
	return after, nil
}

// activeOf counts the rollout's nodes that take a place of maxParallel.
func activeOf(r RolloutRun) int {
	n := 0
	for _, node := range r.Nodes {
		if node.State.Active() {
			n++
		}
	}
	return n
}

// changed reports whether a step changed the rollout.
func changed(a, b RolloutRun) bool {
	if a.State != b.State || a.Reason != b.Reason || len(a.Nodes) != len(b.Nodes) {
		return true
	}
	for i := range a.Nodes {
		if a.Nodes[i] != b.Nodes[i] {
			return true
		}
	}
	return false
}
