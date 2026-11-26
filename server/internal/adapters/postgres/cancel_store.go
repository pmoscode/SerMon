package postgres

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/updates"
	"github.com/pmoscode/SerMon/server/internal/domain/command"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Cancelling and pausing updates by hand (M4-31). Each takes the row of the organization's update
// lease first: a save of the orchestrator in flight ends before, the next one sees the change and
// drops what it planned without it.

// UnderWay implements updates.ManualStore.
func (s *RolloutStore) UnderWay(ctx context.Context, org tenant.OrgID, nodes []identity.NodeID) (updates.UnderWay, error) {
	scope, err := orgScope(org)
	if err != nil {
		return updates.UnderWay{}, err
	}
	out := updates.UnderWay{Rebooting: map[identity.NodeID]bool{}, InRollout: map[identity.NodeID]bool{}}
	ids := make([]uuid.UUID, len(nodes))
	for i, n := range nodes {
		ids[i] = uuid.UUID(n)
	}
	err = s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		paused, err := q.UpdatesPausedAt(ctx)
		if err != nil {
			return wrap(err, "read the pause of the updates")
		}
		counts, err := q.OpenRunsOfOrganization(ctx)
		if err != nil {
			return wrap(err, "count runs under way")
		}
		busy, err := q.NodesUnderWay(ctx, ids)
		if err != nil {
			return wrap(err, "read busy nodes")
		}
		out.Paused, out.Org, out.Instance = paused != nil, int(counts.Org), int(counts.Instance)
		for _, b := range busy {
			if b.What == "reboot" {
				out.Rebooting[identity.NodeID(b.NodeID)] = true
			} else {
				out.InRollout[identity.NodeID(b.NodeID)] = true
			}
		}
		return nil
	})
	return out, err
}

// Rollout implements updates.ManualStore.
func (s *RolloutStore) Rollout(ctx context.Context, scope tenant.Scope, id uuid.UUID) (updates.RolloutRun, bool, error) {
	var out updates.RolloutRun
	found := false
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		r, err := q.GetUpdateRollout(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return wrap(err, "read rollout")
		}
		if !scope.AllGroups() && !slices.Contains(scope.Groups(), tenant.GroupID(r.GroupID)) {
			return nil
		}
		out = updates.RolloutRun{ID: r.ID, Group: tenant.GroupID(r.GroupID), State: updates.RolloutState(r.State), Manual: r.Manual} //nolint:gosec // 1 to 3 by the table's check
		found = true
		return nil
	})
	return out, found, err
}

// CancelRollout implements updates.ManualStore.
func (s *RolloutStore) CancelRollout(ctx context.Context, org tenant.OrgID, id uuid.UUID, by identity.UserID, now time.Time, audit access.AuditEntry) (bool, error) {
	scope, err := orgScope(org)
	if err != nil {
		return false, err
	}
	done := false
	err = s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		if err := q.LockUpdates(ctx); err != nil {
			return wrap(err, "lock the updates")
		}
		_, err := q.CancelUpdateRollout(ctx, sqlcdb.CancelUpdateRolloutParams{
			Reason: "cancelled by hand", At: &now, CancelledBy: optionalUser(by), ID: id,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return wrap(err, "cancel rollout")
		}
		if err := q.SkipWaitingRolloutNodes(ctx, sqlcdb.SkipWaitingRolloutNodesParams{Reason: "the rollout was cancelled", At: now, RolloutID: id}); err != nil {
			return wrap(err, "skip waiting nodes")
		}
		runs, err := q.CancelRolloutRuns(ctx, sqlcdb.CancelRolloutRunsParams{At: &now, CancelledBy: optionalUser(by), RolloutID: id})
		if err != nil {
			return wrap(err, "cancel the rollout's runs")
		}
		for _, r := range runs {
			if err := withdrawn(ctx, q, r.NodeID, r.State); err != nil {
				return err
			}
		}
		done = true
		return appendAudit(ctx, q, audit)
	})
	return done, err
}

// CancelRun implements updates.ManualStore.
func (s *RolloutStore) CancelRun(ctx context.Context, org tenant.OrgID, id uuid.UUID, by identity.UserID, now time.Time, audit access.AuditEntry) (bool, error) {
	scope, err := orgScope(org)
	if err != nil {
		return false, err
	}
	done := false
	err = s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		if err := q.LockUpdates(ctx); err != nil {
			return wrap(err, "lock the updates")
		}
		r, err := q.CancelNodeCommand(ctx, sqlcdb.CancelNodeCommandParams{
			At: &now, CancelledBy: optionalUser(by), ID: id, SentBefore: now.Add(-command.SentGrace),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return wrap(err, "cancel run")
		}
		if err := withdrawn(ctx, q, r.NodeID, r.State); err != nil {
			return err
		}
		done = true
		return appendAudit(ctx, q, audit)
	})
	return done, err
}

// withdrawn wakes the session of a node whose sent run is withdrawn: it tells the agent.
func withdrawn(ctx context.Context, q *sqlcdb.Queries, node uuid.UUID, state int16) error {
	if command.State(state) != command.Sent { //nolint:gosec // 1 to 6 by the table's check
		return nil
	}
	return wrap(q.NotifyNodeCommands(ctx, node), "notify the cancel")
}

// PausedSince implements updates.PolicyStore.
func (s *UpdatePolicyStore) PausedSince(ctx context.Context, org tenant.OrgID) (*time.Time, error) {
	scope, err := orgScope(org)
	if err != nil {
		return nil, err
	}
	var at *time.Time
	err = s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		var err error
		at, err = q.UpdatesPausedAt(ctx)
		return wrap(err, "read the pause of the updates")
	})
	return at, err
}

// SetPaused implements updates.PolicyStore.
func (s *UpdatePolicyStore) SetPaused(ctx context.Context, org tenant.OrgID, at *time.Time, by identity.UserID, audit access.AuditEntry) error {
	scope, err := orgScope(org)
	if err != nil {
		return err
	}
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		if err := q.LockUpdates(ctx); err != nil {
			return wrap(err, "lock the updates")
		}
		params := sqlcdb.SetUpdatesPausedParams{PausedAt: at}
		if at != nil {
			params.PausedBy = optionalUser(by)
		}
		if err := q.SetUpdatesPaused(ctx, params); err != nil {
			return wrap(err, "pause the updates")
		}
		return appendAudit(ctx, q, audit)
	})
}

// Cancels implements agentapi.Commands.
func (s *CommandStore) Cancels(ctx context.Context, org tenant.OrgID, node identity.NodeID, resend bool) ([]uuid.UUID, error) {
	scope, err := orgScope(org)
	if err != nil {
		return nil, err
	}
	var out []uuid.UUID
	err = s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		var err error
		out, err = q.CancelsToSend(ctx, sqlcdb.CancelsToSendParams{NodeID: uuid.UUID(node), Resend: resend})
		return wrap(err, "read cancels")
	})
	return out, err
}

// MarkCancelsSent implements agentapi.Commands.
func (s *CommandStore) MarkCancelsSent(ctx context.Context, org tenant.OrgID, node identity.NodeID, ids []uuid.UUID, now time.Time) error {
	scope, err := orgScope(org)
	if err != nil {
		return err
	}
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		return wrap(q.MarkCancelsSent(ctx, sqlcdb.MarkCancelsSentParams{Now: &now, NodeID: uuid.UUID(node), Ids: ids}), "record sent cancels")
	})
}
