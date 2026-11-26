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

// Limits of the runs under way, by hand and of the rollouts together (M4-31): the orchestrator's
// defaults.
const (
	MaxRunsPerOrg      = 100
	MaxRunsPerInstance = 500
)

var (
	// ErrNotCancellable means the run started, ended or was cancelled before.
	ErrNotCancellable = errors.New("the run started or ended already")
	// ErrRolloutEnded means the rollout doesn't run any more.
	ErrRolloutEnded = errors.New("the rollout ended already")
	// ErrPaused means the organization's updates are paused (M4-31).
	ErrPaused = errors.New("updates are paused in this organization")
	// ErrTooManyRuns means the organization or the instance has too many runs under way.
	ErrTooManyRuns = errors.New("too many runs under way, wait for them to end")
	// ErrUnderWay means a node reboots already or runs in a rollout.
	ErrUnderWay = errors.New("a run is under way on the node")
)

// UnderWay is what runs in the organization now (M4-31).
type UnderWay struct {
	Paused bool
	// Org and Instance count the runs under way: pending, sent and running.
	Org, Instance int
	// Rebooting and InRollout are the asked nodes with a reboot under way or a run in a rollout.
	Rebooting, InRollout map[identity.NodeID]bool
}

// mayRunByHand checks the pause and the limits before count new runs by hand on nodes; exclusive
// runs (reboots, self-updates) also need nodes without a reboot under way or a run in a rollout.
func (s *Service) mayRunByHand(ctx context.Context, org tenant.OrgID, nodes []NodeUpdates, count int, exclusive bool) error {
	ids := make([]identity.NodeID, len(nodes))
	for i, n := range nodes {
		ids[i] = n.Node
	}
	w, err := s.manual.UnderWay(ctx, org, ids)
	if err != nil {
		return fmt.Errorf("read the runs under way: %w", err)
	}
	switch {
	case w.Paused:
		return ErrPaused
	case !exclusive:
		return nil
	case w.Org+count > MaxRunsPerOrg || w.Instance+count > MaxRunsPerInstance:
		return ErrTooManyRuns
	}
	for _, n := range nodes {
		switch {
		case w.Rebooting[n.Node]:
			return fmt.Errorf("%w: %s reboots already", ErrUnderWay, n.Hostname)
		case w.InRollout[n.Node]:
			return fmt.Errorf("%w: %s runs in a rollout", ErrUnderWay, n.Hostname)
		}
	}
	return nil
}

// CancelRun cancels a run the node hasn't started (M4-31, T97): run_updates on the node's group,
// reboot_nodes for a reboot, and a fresh step-up; ErrNotCancellable for one that started or ended.
func (s *Service) CancelRun(ctx context.Context, c access.Caller, id uuid.UUID) error {
	h, err := s.historyStore()
	if err != nil {
		return err
	}
	run, ok, err := h.Run(ctx, c.Principal.Org, id)
	if err != nil {
		return fmt.Errorf("read run: %w", err)
	}
	if !ok || run.Kind == command.Refresh {
		return access.ErrNotFound
	}
	n, err := s.Node(ctx, c, run.Node)
	if err != nil {
		return err
	}
	action := authz.RunUpdates
	if run.Kind == command.Reboot {
		action = authz.RebootNodes
	}
	if !s.policy.Allowed(c.Principal, action, authz.InGroup(c.Principal.Org, n.Group)) {
		return access.ErrForbidden
	}
	now := s.now()
	if now.Sub(c.StepUpAt) > access.StepUpMaxAge {
		return access.ErrStepUpRequired
	}
	if !run.Cancellable(now) {
		return ErrNotCancellable
	}
	if s.manual == nil {
		return errors.New("no manual store")
	}
	audit := access.AuditEntry{
		Actor: c.Principal.User, Action: "node.run_cancelled", Target: "node:" + run.Node.String(),
		Details: map[string]string{"run": run.ID.String(), "kind": run.Kind.String()},
	}
	done, err := s.manual.CancelRun(ctx, c.Principal.Org, id, c.Principal.User, now, audit)
	if err != nil {
		return fmt.Errorf("cancel run: %w", err)
	}
	if !done {
		return ErrNotCancellable
	}
	return nil
}

// CancelRollout stops a running rollout (M4-31, T97): run_updates on its group and a fresh
// step-up. Waiting nodes are skipped, their runs not started yet cancelled; running ones go to
// their end without a reboot.
func (s *Service) CancelRollout(ctx context.Context, c access.Caller, id uuid.UUID) error {
	scope, err := s.scope(c)
	if err != nil {
		return err
	}
	if s.manual == nil {
		return errors.New("no manual store")
	}
	r, ok, err := s.manual.Rollout(ctx, scope, id)
	if err != nil {
		return fmt.Errorf("read rollout: %w", err)
	}
	if !ok {
		return access.ErrNotFound
	}
	if !s.policy.Allowed(c.Principal, authz.RunUpdates, authz.InGroup(c.Principal.Org, r.Group)) {
		return access.ErrForbidden
	}
	now := s.now()
	if now.Sub(c.StepUpAt) > access.StepUpMaxAge {
		return access.ErrStepUpRequired
	}
	if r.State != RolloutRunning {
		return ErrRolloutEnded
	}
	audit := access.AuditEntry{Actor: c.Principal.User, Action: "updates.cancel", Target: "rollout:" + id.String()}
	done, err := s.manual.CancelRollout(ctx, c.Principal.Org, id, c.Principal.User, now, audit)
	if err != nil {
		return fmt.Errorf("cancel rollout: %w", err)
	}
	if !done {
		return ErrRolloutEnded
	}
	return nil
}

// PausedSince returns since when the organization's updates are paused, nil while they run
// (view_nodes on any group).
func (s *PolicyService) PausedSince(ctx context.Context, c access.Caller) (*time.Time, error) {
	if _, err := s.viewScope(c); err != nil {
		return nil, err
	}
	at, err := s.store.PausedSince(ctx, c.Principal.Org)
	if err != nil {
		return nil, fmt.Errorf("read the pause of the updates: %w", err)
	}
	return at, nil
}

// SetPaused pauses or resumes the organization's updates (M4-31, T97): manage_policies and a
// fresh step-up, in the audit log. It returns since when they are paused, nil after a resume.
func (s *PolicyService) SetPaused(ctx context.Context, c access.Caller, paused bool) (*time.Time, error) {
	now := s.now()
	if _, err := access.Authorize(s.policy, c, authz.ManagePolicies, true, now); err != nil {
		return nil, err //nolint:wrapcheck // access errors are mapped by the transport
	}
	var at *time.Time
	action := "updates.resume"
	if paused {
		at, action = &now, "updates.pause"
	}
	if err := s.store.SetPaused(ctx, c.Principal.Org, at, c.Principal.User, access.AuditEntry{Actor: c.Principal.User, Action: action, Target: "organization"}); err != nil {
		return nil, fmt.Errorf("pause the updates: %w", err)
	}
	return at, nil
}
