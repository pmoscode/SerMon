package updates_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/updates"
	"github.com/pmoscode/SerMon/server/internal/domain/command"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// rolloutStore holds per organization one group of nodes; only holder gets its leases.
type rolloutStore struct {
	mu      sync.Mutex
	orgs    []tenant.OrgID
	holder  string
	groups  map[tenant.OrgID][]updates.GroupState
	saved   map[tenant.OrgID][]updates.Change
	saveErr error
	pruned  []tenant.OrgID
	runs    map[uuid.UUID]command.Command
}

func (s *rolloutStore) Organizations(context.Context) ([]tenant.OrgID, error) { return s.orgs, nil }

func (s *rolloutStore) Lease(_ context.Context, _ tenant.OrgID, holder string, _, _ time.Time) (bool, error) {
	return holder == s.holder, nil
}

func (s *rolloutStore) Load(_ context.Context, org tenant.OrgID, _ time.Time) (updates.OrgState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return updates.OrgState{Groups: s.groups[org], Runs: s.runs}, nil
}

func (s *rolloutStore) Save(_ context.Context, org tenant.OrgID, _ string, _ time.Time, changes []updates.Change) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	s.saved[org] = append(s.saved[org], changes...)
	for _, c := range changes {
		for _, a := range c.Actions {
			if a.Command != nil {
				s.runs[a.Command.ID] = *a.Command
			}
		}
	}
	// The next load sees what was saved.
	for i, g := range s.groups[org] {
		for _, c := range changes {
			if c.Rollout.Group == g.Policy.Group {
				r := c.Rollout
				s.groups[org][i].Rollout = &r
			}
		}
	}
	return nil
}

func (s *rolloutStore) Prune(_ context.Context, org tenant.OrgID, _ time.Time) error {
	s.pruned = append(s.pruned, org)
	return nil
}

func started(changes []updates.Change) int {
	n := 0
	for _, c := range changes {
		for _, a := range c.Actions {
			if a.Command != nil && a.Command.Kind == command.Update {
				n++
			}
		}
	}
	return n
}

func orchestrated(t *testing.T, orgs int, nodes int, config func(*updates.OrchestratorConfig)) (*rolloutStore, *updates.Orchestrator) {
	t.Helper()
	store := &rolloutStore{holder: "me", groups: map[tenant.OrgID][]updates.GroupState{}, saved: map[tenant.OrgID][]updates.Change{}, runs: map[uuid.UUID]command.Command{}}
	for i := range orgs {
		org := tenant.OrgID(uuid.New())
		store.orgs = append(store.orgs, org)
		w := newWorld(t, policy(updates.ModeAllAuto, 50, false, false), nodes)
		store.groups[org] = []updates.GroupState{{Policy: w.policy, Nodes: w.nodes}}
		_ = i
	}
	c := updates.DefaultOrchestratorConfig("me", slog.New(slog.DiscardHandler))
	c.Now = func() time.Time { return windowStart }
	config(&c)
	return store, updates.NewOrchestrator(store, c)
}

// The organization's and the instance's limits bound the runs at once; the instance's room goes
// to the organizations in turn.
func TestOrchestrator_LimitsPerOrganizationAndInstance(t *testing.T) {
	store, o := orchestrated(t, 3, 10, func(c *updates.OrchestratorConfig) { c.OrgLimit, c.InstanceLimit = 4, 6 })
	o.Round(context.Background())
	total := 0
	for _, org := range store.orgs {
		n := started(store.saved[org])
		if n > 4 {
			t.Errorf("%v started %d, limit 4", org, n)
		}
		total += n
	}
	if total != 6 {
		t.Errorf("started %d, instance limit 6", total)
	}
	if got := o.Stats().Started.Load(); got != 6 {
		t.Errorf("stats started %d", got)
	}
	if len(store.pruned) != 3 {
		t.Errorf("pruned %v", store.pruned)
	}
	// The next round: all places are taken, nobody starts, no organization is pruned again.
	o.Round(context.Background())
	if total := started(store.saved[store.orgs[0]]) + started(store.saved[store.orgs[1]]) + started(store.saved[store.orgs[2]]); total != 6 {
		t.Errorf("after the second round %d started", total)
	}
	if len(store.pruned) != 3 {
		t.Errorf("pruned again: %v", store.pruned)
	}
}

// Without the lease nothing is planned or saved; a lost lease at the save is counted.
func TestOrchestrator_OnlyTheLeaseHolderPlans(t *testing.T) {
	store, o := orchestrated(t, 1, 2, func(c *updates.OrchestratorConfig) { c.Holder = "other" })
	o.Round(context.Background())
	if len(store.saved) != 0 || len(store.pruned) != 0 {
		t.Errorf("planned without the lease: %v", store.saved)
	}
	store, o = orchestrated(t, 1, 2, func(*updates.OrchestratorConfig) {})
	store.saveErr = updates.ErrLeaseLost
	o.Round(context.Background())
	if o.Stats().LeasesLost.Load() != 1 {
		t.Errorf("leases lost = %d", o.Stats().LeasesLost.Load())
	}
	store.saveErr = errors.New("database down")
	o.Round(context.Background())
	if o.Stats().Errors.Load() != 1 {
		t.Errorf("errors = %d", o.Stats().Errors.Load())
	}
}

func TestOrchestrator_RunEndsWithTheContext(t *testing.T) {
	_, o := orchestrated(t, 1, 1, func(c *updates.OrchestratorConfig) { c.Interval = time.Millisecond })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- o.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil || o.Stats().Rounds.Load() < 2 {
			t.Errorf("Run() = %v after %d rounds", err, o.Stats().Rounds.Load())
		}
	case <-time.After(time.Second):
		t.Fatal("Run() didn't end")
	}
}

// Review M4-30: maxParallel holds for the group across its rollouts – the policy's and one by
// hand – and a node runs in one at a time.
func TestOrchestrator_MaxParallelPerGroupAcrossRollouts(t *testing.T) {
	store, o := orchestrated(t, 1, 3, func(*updates.OrchestratorConfig) {})
	org := store.orgs[0]
	scheduled := store.groups[org][0]
	scheduled.Policy.Rollout.MaxParallel = 2
	manual := scheduled
	manual.Policy.Window = nil
	r := updates.RolloutRun{
		ID: uuid.New(), Group: scheduled.Policy.Group, WindowStart: windowStart, WindowEnd: windowStart.Add(2 * time.Hour),
		State: updates.RolloutRunning, Manual: true, Mode: updates.ModeAllAuto,
	}
	for _, n := range scheduled.Nodes {
		r.Nodes = append(r.Nodes, updates.NodeRun{Node: n.ID, Hostname: n.Hostname, State: updates.NodeWaiting, Since: windowStart})
	}
	manual.Rollout = &r
	store.groups[org] = []updates.GroupState{scheduled, manual}
	o.Round(context.Background())
	if n := started(store.saved[org]); n != 2 {
		t.Errorf("started %d runs in the group, maxParallel 2", n)
	}
	seen := map[uuid.UUID]int{}
	for _, c := range store.saved[org] {
		for _, a := range c.Actions {
			if a.Command != nil {
				seen[uuid.UUID(a.Node)]++
			}
		}
	}
	for node, n := range seen {
		if n > 1 {
			t.Errorf("%v got %d runs", node, n)
		}
	}
}

// Review M4-30: a node active in one rollout waits in the others, even with room in them – the
// next one of the group starts instead.
func TestOrchestrator_ANodeRunsInOneRolloutAtATime(t *testing.T) {
	store, o := orchestrated(t, 1, 3, func(*updates.OrchestratorConfig) {})
	org := store.orgs[0]
	scheduled := store.groups[org][0]
	scheduled.Policy.Rollout.MaxParallel = 2
	busy := scheduled.Nodes[2] // web-01, the first by host name
	run := command.Command{ID: uuid.New(), Node: busy.ID, Kind: command.Update, Scope: command.ScopeAll, State: command.Sent, CreatedAt: windowStart, ExpiresAt: windowStart.Add(time.Hour)}
	store.runs[run.ID] = run
	manual := scheduled
	manual.Policy.Window = nil
	manual.Rollout = &updates.RolloutRun{
		ID: uuid.New(), Group: scheduled.Policy.Group, WindowStart: windowStart, WindowEnd: windowStart.Add(2 * time.Hour),
		State: updates.RolloutRunning, Manual: true, Mode: updates.ModeAllAuto,
		Nodes: []updates.NodeRun{{Node: busy.ID, Hostname: busy.Hostname, State: updates.NodeUpdating, UpdateRun: run.ID, Since: windowStart}},
	}
	store.groups[org] = []updates.GroupState{scheduled, manual}
	o.Round(context.Background())
	var nodes []string
	for _, c := range store.saved[org] {
		for _, a := range c.Actions {
			if a.Command != nil && a.Command.Kind == command.Update {
				nodes = append(nodes, a.Node.String())
			}
		}
	}
	if len(nodes) != 1 || nodes[0] == busy.ID.String() {
		t.Errorf("started %v; %v runs already", nodes, busy.ID)
	}
}

// M4-31: an organization that is gone (deleted or locked) doesn't keep its places of the instance.
func TestOrchestrator_AGoneOrganizationFreesItsPlaces(t *testing.T) {
	store, o := orchestrated(t, 2, 10, func(c *updates.OrchestratorConfig) { c.OrgLimit, c.InstanceLimit = 4, 4 })
	o.Round(context.Background())
	var full, other tenant.OrgID
	for _, org := range store.orgs {
		if started(store.saved[org]) == 4 {
			full = org
		} else {
			other = org
		}
	}
	if full == (tenant.OrgID{}) || started(store.saved[other]) != 0 {
		t.Fatalf("first round: %d and %d", started(store.saved[store.orgs[0]]), started(store.saved[store.orgs[1]]))
	}
	store.mu.Lock()
	store.orgs = []tenant.OrgID{other}
	store.mu.Unlock()
	o.Round(context.Background())
	if n := started(store.saved[other]); n != 4 {
		t.Errorf("after the other organization went, %d started, want the instance's 4", n)
	}
}

// M4-31: agent updates take places of the organization and the instance like package updates,
// across the groups of an organization.
func TestOrchestrator_AgentUpdatesCountAgainstTheLimits(t *testing.T) {
	store, o := orchestrated(t, 1, 0, func(c *updates.OrchestratorConfig) { c.OrgLimit = 4 })
	org := store.orgs[0]
	var groups []updates.GroupState
	for g := range 2 {
		w := newWorld(t, agentPolicy(updates.ModeReport), 6)
		w.policy.Group = tenant.GroupID(uuid.New())
		w.policy.Rollout.MaxParallel = 50
		for i := range w.nodes {
			w.nodes[i].ID = nodeID(100*(g+1) + i)
			w.nodes[i].AgentTarget = "1.1.0"
			w.nodes[i].Report.Permissions.SelfUpdate = true
		}
		groups = append(groups, updates.GroupState{Policy: w.policy, Nodes: w.nodes})
	}
	store.groups[org] = groups
	o.Round(context.Background())
	n := 0
	for _, c := range store.saved[org] {
		for _, a := range c.Actions {
			if a.Command != nil && a.Command.Kind == command.SelfUpdate {
				n++
			}
		}
	}
	if n != 4 {
		t.Errorf("%d agent updates started across two groups, organization limit 4", n)
	}
}
