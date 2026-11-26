package postgres

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/ingest"
	"github.com/pmoscode/SerMon/server/internal/app/routing"
	"github.com/pmoscode/SerMon/server/internal/app/updates"
	"github.com/pmoscode/SerMon/server/internal/domain/command"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/secevent"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	"github.com/pmoscode/SerMon/server/internal/domain/update"
)

// RolloutStore is the update orchestrator's store (M4-13).
type RolloutStore struct {
	db     *DB
	alerts *AlertStore
}

var (
	_ updates.RolloutStore = (*RolloutStore)(nil)
	_ updates.ManualStore  = (*RolloutStore)(nil)
)

// NewRolloutStore returns the store on db.
func NewRolloutStore(db *DB) *RolloutStore { return &RolloutStore{db: db, alerts: NewAlertStore(db)} }

// Organizations implements updates.RolloutStore: the organizations that are not locked.
func (s *RolloutStore) Organizations(ctx context.Context) ([]tenant.OrgID, error) {
	return s.alerts.Organizations(ctx)
}

// Lease implements updates.RolloutStore.
func (s *RolloutStore) Lease(ctx context.Context, org tenant.OrgID, holder string, leaseFor time.Duration) (bool, error) {
	scope, err := orgScope(org)
	if err != nil {
		return false, err
	}
	var n int64
	err = s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		rows, leaseErr := q.LeaseUpdateOrganization(ctx, sqlcdb.LeaseUpdateOrganizationParams{Holder: holder, LeaseSeconds: leaseFor.Seconds()})
		n = rows
		return wrap(leaseErr, "lease updates")
	})
	return n == 1, err
}

// Prune implements updates.RolloutStore.
func (s *RolloutStore) Prune(ctx context.Context, org tenant.OrgID, before time.Time) error {
	scope, err := orgScope(org)
	if err != nil {
		return err
	}
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		if err := q.PruneNodeCommands(ctx, before); err != nil {
			return wrap(err, "prune commands")
		}
		return wrap(q.PruneUpdateRollouts(ctx, before), "prune rollouts")
	})
}

// Load implements updates.RolloutStore.
func (s *RolloutStore) Load(ctx context.Context, org tenant.OrgID, now time.Time) (updates.OrgState, error) {
	scope, err := orgScope(org)
	if err != nil {
		return updates.OrgState{}, err
	}
	out := updates.OrgState{Runs: map[uuid.UUID]command.Command{}}
	err = s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		policies, err := q.ListUpdatePolicies(ctx, sqlcdb.ListUpdatePoliciesParams{AllGroups: true})
		if err != nil {
			return wrap(err, "list update policies")
		}
		pausedAt, err := q.UpdatesPausedAt(ctx)
		if err != nil {
			return wrap(err, "read the pause of the updates")
		}
		paused := pausedAt != nil
		latest, err := q.LatestUpdateRollouts(ctx)
		if err != nil {
			return wrap(err, "list rollouts")
		}
		rollouts := map[tenant.GroupID]*updates.RolloutRun{}
		ids := make([]uuid.UUID, 0, len(latest))
		for _, r := range latest {
			rollouts[tenant.GroupID(r.GroupID)] = &updates.RolloutRun{
				ID: r.ID, Group: tenant.GroupID(r.GroupID), WindowStart: r.WindowStart, WindowEnd: r.WindowEnd,
				State: updates.RolloutState(r.State), Reason: r.Reason, Cancelled: r.CancelledAt != nil, //nolint:gosec // 1 to 3 by the table's check
			}
			ids = append(ids, r.ID)
		}
		byID := map[uuid.UUID]*updates.RolloutRun{}
		for _, r := range rollouts {
			byID[r.ID] = r
		}
		manual, err := q.RunningManualRollouts(ctx)
		if err != nil {
			return wrap(err, "list rollouts by hand")
		}
		manuals := make([]*updates.RolloutRun, 0, len(manual))
		for _, m := range manual {
			r := &updates.RolloutRun{
				ID: m.ID, Group: tenant.GroupID(m.GroupID), WindowStart: m.WindowStart, WindowEnd: m.WindowEnd,
				State: updates.RolloutState(m.State), Reason: m.Reason, Manual: true, //nolint:gosec // 1 to 3 by the table's check
				Mode: updates.Mode(m.Mode.Int16), AutoReboot: m.AutoReboot, Cancelled: m.CancelledAt != nil, //nolint:gosec // 2 or 3 by the table's check
			}
			manuals = append(manuals, r)
			byID[r.ID] = r
			ids = append(ids, r.ID)
		}
		nodes, err := q.ListUpdateRolloutNodes(ctx, ids)
		if err != nil {
			return wrap(err, "list rollout nodes")
		}
		var runs []uuid.UUID
		for _, n := range nodes {
			run := updates.NodeRun{
				Node: identity.NodeID(n.NodeID), Hostname: n.Hostname, State: updates.NodeState(n.State), Reason: n.Reason, //nolint:gosec // 1 to 7 by the table's check
				Since: n.Since,
			}
			if n.UpdateRun != nil {
				run.UpdateRun = *n.UpdateRun
				runs = append(runs, run.UpdateRun)
			}
			if n.RebootRun != nil {
				run.RebootRun = *n.RebootRun
				runs = append(runs, run.RebootRun)
			}
			if n.AgentRun != nil {
				run.AgentRun = *n.AgentRun
				runs = append(runs, run.AgentRun)
			}
			if n.Deadline != nil {
				run.Deadline = *n.Deadline
			}
			if r := byID[n.RolloutID]; r != nil {
				r.Nodes = append(r.Nodes, run)
			}
		}
		views, err := q.ListRolloutNodeViews(ctx)
		if err != nil {
			return wrap(err, "list nodes")
		}
		byGroup := map[tenant.GroupID][]updates.NodeView{}
		for _, v := range views {
			byGroup[tenant.GroupID(v.GroupID)] = append(byGroup[tenant.GroupID(v.GroupID)], nodeView(v, now))
		}
		groupPolicy := map[tenant.GroupID]updates.Policy{}
		for _, row := range policies {
			p := policyOf(row).Policy
			p.Paused = paused
			groupPolicy[p.Group] = p
			r := rollouts[p.Group]
			if p.Mode == updates.ModeReport && p.AgentUpdate != updates.AgentAuto && (r == nil || !r.Following()) {
				continue
			}
			out.Groups = append(out.Groups, updates.GroupState{Policy: p, Rollout: r, Nodes: byGroup[p.Group]})
		}
		// A rollout by hand runs with its own mode and reboot, the rest of the group's policy.
		for _, r := range manuals {
			p, ok := groupPolicy[r.Group]
			if !ok {
				p = updates.DefaultPolicy(r.Group)
				p.Paused = paused
			}
			// By hand only the packages: agents are updated by hand on their own (UpdateAgents).
			p.Mode, p.AutoReboot, p.Window, p.AgentUpdate = r.Mode, r.AutoReboot, nil, updates.AgentManual
			out.Groups = append(out.Groups, updates.GroupState{Policy: p, Rollout: r, Nodes: byGroup[r.Group]})
		}
		commands, err := q.ListNodeCommandsByID(ctx, runs)
		if err != nil {
			return wrap(err, "list runs")
		}
		for _, c := range commands {
			out.Runs[c.ID] = commandOf(c)
		}
		return nil
	})
	return out, err
}

func nodeView(v sqlcdb.ListRolloutNodeViewsRow, now time.Time) updates.NodeView {
	out := updates.NodeView{
		ID: identity.NodeID(v.ID), Hostname: v.Hostname, Online: ingest.Online(v.LastSeenAt, now), Critical: v.Critical,
		Agent: update.Agent{Version: v.AgentVersion, Method: update.InstallMethod(v.InstallMethod), Arch: v.Architecture}, //nolint:gosec // 0 to 2 by the table's check
	}
	if v.ConnectedAt != nil {
		out.ConnectedAt = *v.ConnectedAt
	}
	if v.ReceivedAt != nil {
		out.ReportAt = *v.ReceivedAt
		r := &update.Report{
			Availability: update.Availability(v.Availability.Int16), Manager: update.Manager(v.PackageManager.Int16), //nolint:gosec // checked by the table
			Total: uint32(v.TotalCount.Int32), Security: uint32(v.SecurityCount.Int32), RebootRequired: v.RebootRequired.Bool, //nolint:gosec // not negative by the table's check
		}
		if v.AllowRemoteUpdates.Valid {
			r.Permissions = &update.Permissions{
				RemoteUpdates: v.AllowRemoteUpdates.Bool, RemoteReboot: v.AllowRemoteReboot.Bool, SelfUpdate: v.AllowSelfUpdate.Bool,
			}
		}
		out.Report = r
	}
	return out
}

func commandOf(c sqlcdb.ListNodeCommandsByIDRow) command.Command {
	out := command.Command{
		ID: c.ID, Node: identity.NodeID(c.NodeID), Kind: command.Kind(c.Kind), Scope: command.Scope(c.Scope.Int16), //nolint:gosec // checked by the table
		Reason: c.Reason, Version: c.Version, State: command.State(c.State), //nolint:gosec // checked by the table
		CreatedAt: c.CreatedAt, ExpiresAt: c.ExpiresAt, CreatedBy: userOf(c.CreatedBy), SentAt: c.SentAt, CancelledAt: c.CancelledAt,
	}
	if c.Phase.Valid {
		out.Result = &command.Result{
			Phase: command.Phase(c.Phase.Int16), ExitCode: c.ExitCode.Int32, Reason: c.ResultReason, //nolint:gosec // 1 to 6
			StartedAt: c.StartedAt, EndedAt: c.EndedAt,
		}
	}
	return out
}

func optionalID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

// Save implements updates.RolloutStore.
func (s *RolloutStore) Save(ctx context.Context, org tenant.OrgID, holder string, now time.Time, changes []updates.Change) error {
	scope, err := orgScope(org)
	if err != nil {
		return err
	}
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		held, err := q.HoldUpdateLease(ctx, holder)
		if err != nil {
			return wrap(err, "hold update lease")
		}
		if held == 0 {
			return updates.ErrLeaseLost
		}
		// A cancel or a pause by hand since the load: it took the lease's row before, the plan
		// didn't know it (M4-31).
		pausedAt, err := q.UpdatesPausedAt(ctx)
		if err != nil {
			return wrap(err, "read the pause of the updates")
		}
		ids := make([]uuid.UUID, len(changes))
		for i, c := range changes {
			ids[i] = c.Rollout.ID
		}
		cancelled, err := q.CancelledRollouts(ctx, ids)
		if err != nil {
			return wrap(err, "read cancelled rollouts")
		}
		for _, c := range changes {
			if slices.Contains(cancelled, c.Rollout.ID) && !c.Rollout.Cancelled || pausedAt != nil && sendsCommand(c) {
				continue
			}
			if err := saveRollout(ctx, q, c.Rollout, now); err != nil {
				return err
			}
			for _, a := range c.Actions {
				if err := apply(ctx, q, a, now); err != nil {
					return err
				}
				if err := auditCommand(ctx, q, c.Rollout, a); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func sendsCommand(c updates.Change) bool {
	return slices.ContainsFunc(c.Actions, func(a updates.Action) bool { return a.Command != nil })
}

// auditCommand notes a run the rollout sends (T41, review M4-30): which rollout, by policy or
// by whom; the policy's changes have their own entries.
func auditCommand(ctx context.Context, q *sqlcdb.Queries, r updates.RolloutRun, a updates.Action) error {
	if a.Command == nil {
		return nil
	}
	details := map[string]string{"run": a.Command.ID.String(), "kind": a.Command.Kind.String(), "rollout": r.ID.String(), "by": "update policy"}
	if r.Manual {
		details["by"] = "rollout by hand"
	}
	if a.Command.Version != "" {
		details["version"] = a.Command.Version
	}
	return appendAudit(ctx, q, access.AuditEntry{
		Actor: r.CreatedBy, Action: "node.command", Target: "node:" + a.Command.Node.String(), Details: details,
	})
}

func saveRollout(ctx context.Context, q *sqlcdb.Queries, r updates.RolloutRun, now time.Time) error {
	params := sqlcdb.UpsertUpdateRolloutParams{
		ID: r.ID, GroupID: uuid.UUID(r.Group), WindowStart: r.WindowStart, WindowEnd: r.WindowEnd,
		State: int16(r.State), Reason: bounded(r.Reason, 1024), Now: now, Manual: r.Manual, AutoReboot: r.AutoReboot,
		CreatedBy: optionalUser(r.CreatedBy),
	}
	if r.Manual {
		params.Mode = pgtype.Int2{Int16: int16(r.Mode), Valid: true}
	}
	if err := q.UpsertUpdateRollout(ctx, params); err != nil {
		return wrap(err, "store rollout")
	}
	if err := q.DeleteUpdateRolloutNodes(ctx, r.ID); err != nil {
		return wrap(err, "store rollout nodes")
	}
	for i, n := range r.Nodes {
		if err := q.InsertUpdateRolloutNode(ctx, sqlcdb.InsertUpdateRolloutNodeParams{
			RolloutID: r.ID, NodeID: uuid.UUID(n.Node), Position: int32(i), Hostname: n.Hostname,
			State: int16(n.State), Reason: bounded(n.Reason, 1024), UpdateRun: optionalID(n.UpdateRun), RebootRun: optionalID(n.RebootRun),
			Since: n.Since, Deadline: optionalTime(n.Deadline), AgentRun: optionalID(n.AgentRun),
		}); err != nil {
			return wrap(err, "store rollout node")
		}
	}
	return nil
}

// rolloutSilence is the comment of the silences a rollout sets: they have no creator.
const rolloutSilence = "updates of the rollout (SerMon)"

// apply carries out an action of a rollout step in the save's transaction.
func apply(ctx context.Context, q *sqlcdb.Queries, a updates.Action, now time.Time) error {
	if a.Command != nil {
		if err := insertCommand(ctx, q, *a.Command); err != nil {
			return err
		}
	}
	if !a.SilenceUntil.IsZero() {
		rules := make([]string, 0, len(routing.MaintenanceRules))
		for _, r := range routing.MaintenanceRules {
			rules = append(rules, string(r))
		}
		match, err := encodeMatchers(routing.Matchers{Rules: rules})
		if err != nil {
			return err
		}
		if err := q.InsertAlertSilence(ctx, sqlcdb.InsertAlertSilenceParams{
			ID: uuid.Must(uuid.NewV7()), Matchers: match, NodeIds: []uuid.UUID{uuid.UUID(a.Node)}, StartsAt: now, EndsAt: a.SilenceUntil,
			Comment: rolloutSilence, CreatedAt: now,
		}); err != nil {
			return wrap(err, "silence the node")
		}
		if err := appendAudit(ctx, q, access.AuditEntry{
			Action: "silence.create", Target: "node:" + a.Node.String(),
			Details: map[string]string{"by": "update rollout", "until": a.SilenceUntil.UTC().Format(time.RFC3339)},
		}); err != nil {
			return err
		}
	}
	if a.EndSilence {
		n, err := q.EndRolloutSilences(ctx, sqlcdb.EndRolloutSilencesParams{At: now, NodeID: uuid.UUID(a.Node), Comment: rolloutSilence})
		if err != nil {
			return wrap(err, "end the node's silence")
		}
		if n > 0 {
			if err := appendAudit(ctx, q, access.AuditEntry{
				Action: "silence.expire", Target: "node:" + a.Node.String(), Details: map[string]string{"by": "update rollout"},
			}); err != nil {
				return err
			}
		}
	}
	if a.Event != "" {
		kind := secevent.UpdateRunFailed
		if a.Event == updates.EventRolloutStopped {
			kind = secevent.UpdateRolloutStopped
		}
		e := secevent.Event{Node: a.Node, OccurredAt: now, Kind: kind, Details: secevent.Details{Reason: bounded(a.Reason, secevent.MaxReason)}}
		if err := insertServerEvent(ctx, q, e.Normalized()); err != nil {
			return fmt.Errorf("rollout event: %w", err)
		}
	}
	return nil
}

// StartRollouts implements updates.ManualStore: the rollouts by hand with their audit entry.
func (s *RolloutStore) StartRollouts(ctx context.Context, org tenant.OrgID, rollouts []updates.RolloutRun, now time.Time, audit access.AuditEntry) error {
	scope, err := orgScope(org)
	if err != nil {
		return err
	}
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		for _, r := range rollouts {
			if err := saveRollout(ctx, q, r, now); err != nil {
				return err
			}
		}
		return appendAudit(ctx, q, audit)
	})
}

// Enqueue implements updates.ManualStore: commands (reboots, self-updates) with their audit entry.
func (s *RolloutStore) Enqueue(ctx context.Context, org tenant.OrgID, commands []command.Command, audit access.AuditEntry) error {
	scope, err := orgScope(org)
	if err != nil {
		return err
	}
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		for _, c := range commands {
			if err := insertCommand(ctx, q, c); err != nil {
				return err
			}
		}
		return appendAudit(ctx, q, audit)
	})
}
