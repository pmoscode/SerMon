package updates

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/command"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Bounds of the history (M4-15).
const (
	MaxRuns     = 100
	MaxRollouts = 50
)

// NamedRollout is a rollout with the name of its group and when it was created.
type NamedRollout struct {
	RolloutRun
	GroupName string
	CreatedAt time.Time
}

// History reads the runs and rollouts of the past (M4-15).
type History interface {
	// Runs returns the node's runs (no refreshes), newest first, without output.
	Runs(ctx context.Context, org tenant.OrgID, node identity.NodeID, limit int) ([]command.Command, error)
	// Run returns a run of the organization with its output; false if there is none.
	Run(ctx context.Context, org tenant.OrgID, id uuid.UUID) (command.Command, bool, error)
	// Rollouts returns the latest rollouts of the scope's groups with their nodes, newest first.
	Rollouts(ctx context.Context, scope tenant.Scope, limit int) ([]NamedRollout, error)
}

// WithHistory lets the service read the runs and rollouts of the past.
func (s *Service) WithHistory(h History) *Service {
	s.history = h
	return s
}

func (s *Service) historyStore() (History, error) {
	if s.history == nil {
		return nil, errors.New("no history")
	}
	return s.history, nil
}

// Runs returns a visible node's runs, newest first (view_nodes), at most limit (1 to MaxRuns).
func (s *Service) Runs(ctx context.Context, c access.Caller, node identity.NodeID, limit int) ([]command.Command, error) {
	if _, err := s.Node(ctx, c, node); err != nil {
		return nil, err
	}
	h, err := s.historyStore()
	if err != nil {
		return nil, err
	}
	runs, err := h.Runs(ctx, c.Principal.Org, node, min(max(limit, 1), MaxRuns))
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	now := s.now()
	for i := range runs {
		runs[i].State = runs[i].EffectiveState(now)
	}
	return runs, nil
}

// Run returns a run with its output. The output is like a log of the host: it needs view_logs on
// the node's group (access.ErrForbidden); a run of a node the caller can't view is not found.
func (s *Service) Run(ctx context.Context, c access.Caller, id uuid.UUID) (command.Command, error) {
	h, err := s.historyStore()
	if err != nil {
		return command.Command{}, err
	}
	run, ok, err := h.Run(ctx, c.Principal.Org, id)
	if err != nil {
		return command.Command{}, fmt.Errorf("read run: %w", err)
	}
	if !ok || run.Kind == command.Refresh {
		return command.Command{}, access.ErrNotFound
	}
	n, err := s.Node(ctx, c, run.Node)
	if err != nil {
		return command.Command{}, err
	}
	if !s.policy.Allowed(c.Principal, authz.ViewLogs, authz.InGroup(c.Principal.Org, n.Group)) {
		return command.Command{}, access.ErrForbidden
	}
	run.State = run.EffectiveState(s.now())
	return run, nil
}

// Rollouts returns the latest rollouts of the groups the caller can view (view_nodes).
func (s *Service) Rollouts(ctx context.Context, c access.Caller, limit int) ([]NamedRollout, error) {
	scope, err := s.scope(c)
	if err != nil {
		return nil, err
	}
	h, err := s.historyStore()
	if err != nil {
		return nil, err
	}
	out, err := h.Rollouts(ctx, scope, min(max(limit, 1), MaxRollouts))
	if err != nil {
		return nil, fmt.Errorf("list rollouts: %w", err)
	}
	return out, nil
}
