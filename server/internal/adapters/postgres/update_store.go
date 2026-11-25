package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/ingest"
	"github.com/pmoscode/SerMon/server/internal/app/routing"
	"github.com/pmoscode/SerMon/server/internal/app/updates"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	"github.com/pmoscode/SerMon/server/internal/domain/update"
)

// UpdateStore stores the update reports of the nodes and reads them within a scope (M4-09).
type UpdateStore struct {
	db *DB
	// orgPackages bounds the packages stored for all nodes of an organization.
	orgPackages int
}

var (
	_ ingest.UpdateWriter = (*UpdateStore)(nil)
	_ updates.Store       = (*UpdateStore)(nil)
)

// NewUpdateStore returns the store on db; orgPackages ≤ 0 is updates.DefaultOrgPackageLimit.
func NewUpdateStore(db *DB, orgPackages int) *UpdateStore {
	if orgPackages <= 0 {
		orgPackages = updates.DefaultOrgPackageLimit
	}
	return &UpdateStore{db: db, orgPackages: orgPackages}
}

func optionalBool(set bool, v bool) pgtype.Bool { return pgtype.Bool{Bool: v, Valid: set} }

// Write replaces the node's report in one transaction unless the stored one is as new or newer.
// The packages beyond the organization's limit are left out (the security updates come first).
func (s *UpdateStore) Write(ctx context.Context, org tenant.OrgID, node identity.NodeID, r update.Report, receivedAt time.Time) (bool, error) {
	scope, err := tenant.ForAllGroups(org)
	if err != nil {
		return false, fmt.Errorf("write update report: %w", err)
	}
	stored := false
	err = s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		stored = false
		others, err := q.CountOtherNodeUpdatePackages(ctx, uuid.UUID(node))
		if err != nil {
			return wrap(err, "count update packages")
		}
		packages := r.Packages[:min(len(r.Packages), max(s.orgPackages-int(others), 0))]
		p := r.Permissions
		_, err = q.UpsertNodeUpdateState(ctx, sqlcdb.UpsertNodeUpdateStateParams{
			NodeID: uuid.UUID(node), CollectedAt: r.CollectedAt, ReceivedAt: receivedAt,
			Availability: int16(r.Availability), Reason: r.Reason, PackageManager: int16(r.Manager),
			TotalCount: int32(r.Total), SecurityCount: int32(r.Security), PackagesStored: int32(len(packages)), //nolint:gosec // bounded by update.Report.Valid
			RebootRequired: r.RebootRequired, RebootReason: r.RebootReason, LastUpgradeAt: r.LastUpgradeAt,
			AllowRemoteUpdates: optionalBool(p != nil, p != nil && p.RemoteUpdates),
			AllowRemoteReboot:  optionalBool(p != nil, p != nil && p.RemoteReboot),
			AllowSelfUpdate:    optionalBool(p != nil, p != nil && p.SelfUpdate),
			Refreshed:          r.Refreshed,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // the stored report is as new or newer
		}
		if err != nil {
			return wrap(err, "store update state")
		}
		stored = true
		params := sqlcdb.UpsertNodeUpdatePackagesParams{
			NodeID: uuid.UUID(node), SeenAt: r.CollectedAt, Names: make([]string, 0, len(packages)),
			Candidates: make([]string, 0, len(packages)), Installed: make([]string, 0, len(packages)),
			Sources: make([]string, 0, len(packages)), Security: make([]bool, 0, len(packages)),
		}
		for _, p := range packages {
			params.Names = append(params.Names, p.Name)
			params.Candidates = append(params.Candidates, p.Candidate)
			params.Installed = append(params.Installed, p.Installed)
			params.Sources = append(params.Sources, p.Source)
			params.Security = append(params.Security, p.Security)
		}
		if err := q.DeleteGoneNodeUpdatePackages(ctx, sqlcdb.DeleteGoneNodeUpdatePackagesParams{
			NodeID: uuid.UUID(node), Names: params.Names, Candidates: params.Candidates,
		}); err != nil {
			return wrap(err, "delete gone update packages")
		}
		if len(packages) > 0 {
			if err := q.UpsertNodeUpdatePackages(ctx, params); err != nil {
				return wrap(err, "store update packages")
			}
		}
		return wrap(q.SetOldestSecurityUpdate(ctx, uuid.UUID(node)), "store oldest security update")
	})
	if err != nil {
		return false, err
	}
	return stored, nil
}

// Gauge implements ingest.UpdateWriter: the stored report for the derived series (M4-16).
func (s *UpdateStore) Gauge(ctx context.Context, org tenant.OrgID, node identity.NodeID) (update.Gauge, bool, error) {
	scope, err := tenant.ForAllGroups(org)
	if err != nil {
		return update.Gauge{}, false, fmt.Errorf("read update gauge: %w", err)
	}
	var out update.Gauge
	found := false
	err = s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		row, err := q.GetNodeUpdateGauge(ctx, uuid.UUID(node))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return wrap(err, "read update gauge")
		}
		found = true
		out = update.Gauge{
			Listed: update.Availability(row.Availability) == update.Listed, //nolint:gosec // 1 to 5 by the table's check
			Total:  uint32(max(row.TotalCount, 0)), Security: uint32(max(row.SecurityCount, 0)),
			RebootRequired: row.RebootRequired,
		}
		if row.OldestSecurityAt != nil {
			out.OldestSecurity = *row.OldestSecurityAt
		}
		if row.RebootRequiredSince != nil {
			out.RebootSince = *row.RebootRequiredSince
		}
		return nil
	})
	return out, found, err
}

// Node returns a node of the scope with its report and packages.
func (s *UpdateStore) Node(ctx context.Context, scope tenant.Scope, node identity.NodeID) (updates.NodeUpdates, bool, error) {
	var out updates.NodeUpdates
	found := false
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		row, err := q.GetNodeUpdates(ctx, sqlcdb.GetNodeUpdatesParams{
			NodeID: uuid.UUID(node), AllGroups: scope.AllGroups(), GroupIds: groupIDs(scope),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return wrap(err, "read node updates")
		}
		found, out = true, nodeUpdates(row)
		if out.Report == nil {
			return nil
		}
		rows, err := q.ListNodeUpdatePackages(ctx, uuid.UUID(node))
		if err != nil {
			return wrap(err, "list update packages")
		}
		out.Report.Packages = make([]update.Package, 0, len(rows))
		for _, p := range rows {
			out.Report.Packages = append(out.Report.Packages, update.Package{
				Name: p.Name, Installed: p.InstalledVersion, Candidate: p.CandidateVersion, Source: p.Source,
				Security: p.Security, FirstSeenAt: p.FirstSeenAt,
			})
		}
		return nil
	})
	return out, found, err
}

// Fleet returns the nodes of the scope with their reports, without packages.
func (s *UpdateStore) Fleet(ctx context.Context, scope tenant.Scope) ([]updates.NodeUpdates, error) {
	var out []updates.NodeUpdates
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		rows, err := q.ListFleetUpdates(ctx, sqlcdb.ListFleetUpdatesParams{AllGroups: scope.AllGroups(), GroupIds: groupIDs(scope)})
		if err != nil {
			return wrap(err, "list fleet updates")
		}
		out = make([]updates.NodeUpdates, 0, len(rows))
		for _, row := range rows {
			out = append(out, nodeUpdates(sqlcdb.GetNodeUpdatesRow(row)))
		}
		return nil
	})
	return out, err
}

// nodeUpdates converts a node with its report; without a report its columns are NULL.
func nodeUpdates(row sqlcdb.GetNodeUpdatesRow) updates.NodeUpdates {
	n := updates.NodeUpdates{Node: identity.NodeID(row.ID), Hostname: row.Hostname, Group: tenant.GroupID(row.GroupID)}
	if row.CollectedAt == nil {
		return n
	}
	r := &update.Report{
		CollectedAt: *row.CollectedAt, Availability: update.Availability(row.Availability.Int16), //nolint:gosec // 1 to 5 by the table's check
		Reason: row.Reason.String, Manager: update.Manager(row.PackageManager.Int16), //nolint:gosec // 0 to 3 by the table's check
		Total: uint32(row.TotalCount.Int32), Security: uint32(row.SecurityCount.Int32), //nolint:gosec // not negative by the table's check
		RebootRequired: row.RebootRequired.Bool, RebootReason: row.RebootReason.String, LastUpgradeAt: row.LastUpgradeAt,
		Refreshed: row.Refreshed.Bool,
	}
	if row.AllowRemoteUpdates.Valid {
		r.Permissions = &update.Permissions{
			RemoteUpdates: row.AllowRemoteUpdates.Bool, RemoteReboot: row.AllowRemoteReboot.Bool, SelfUpdate: row.AllowSelfUpdate.Bool,
		}
	}
	n.Report = r
	if row.ReceivedAt != nil {
		n.ReceivedAt = *row.ReceivedAt
	}
	n.PackagesOmitted = int64(row.PackagesStored.Int32) < int64(row.TotalCount.Int32)
	n.OldestSecurity = row.OldestSecurityAt
	return n
}

// UpdatePolicyStore keeps the update policies of the groups (M4-10).
type UpdatePolicyStore struct{ db *DB }

var _ updates.PolicyStore = (*UpdatePolicyStore)(nil)

// NewUpdatePolicyStore returns the store on db.
func NewUpdatePolicyStore(db *DB) *UpdatePolicyStore { return &UpdatePolicyStore{db: db} }

// Policy implements updates.PolicyStore.
func (s *UpdatePolicyStore) Policy(ctx context.Context, scope tenant.Scope, group tenant.GroupID) (updates.Policy, bool, error) {
	var out updates.Policy
	found := false
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		row, err := q.GetUpdatePolicy(ctx, sqlcdb.GetUpdatePolicyParams{GroupID: uuid.UUID(group), AllGroups: scope.AllGroups(), GroupIds: groupIDs(scope)})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return wrap(err, "read update policy")
		}
		found, out = true, policyOf(sqlcdb.ListUpdatePoliciesRow(row)).Policy
		return nil
	})
	return out, found, err
}

// Policies implements updates.PolicyStore.
func (s *UpdatePolicyStore) Policies(ctx context.Context, scope tenant.Scope) ([]updates.NamedPolicy, error) {
	var out []updates.NamedPolicy
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		rows, err := q.ListUpdatePolicies(ctx, sqlcdb.ListUpdatePoliciesParams{AllGroups: scope.AllGroups(), GroupIds: groupIDs(scope)})
		if err != nil {
			return wrap(err, "list update policies")
		}
		out = make([]updates.NamedPolicy, 0, len(rows))
		for _, row := range rows {
			out = append(out, policyOf(row))
		}
		return nil
	})
	return out, err
}

func policyOf(row sqlcdb.ListUpdatePoliciesRow) updates.NamedPolicy {
	group := tenant.GroupID(row.ID)
	if !row.Mode.Valid {
		return updates.NamedPolicy{Policy: updates.DefaultPolicy(group), GroupName: row.Name}
	}
	p := updates.Policy{
		Group: group, Mode: updates.Mode(row.Mode.Int16), AutoReboot: row.AutoReboot.Bool, //nolint:gosec // 1 to 3 by the table's check
		Rollout: updates.Rollout{
			MaxParallel: int(row.MaxParallel.Int32), StopOnFailure: row.StopOnFailure.Bool,
			HealthCheck: time.Duration(row.HealthCheckMinutes.Int32) * time.Minute,
		},
		UpdatedBy: userOf(row.UpdatedBy),
	}
	if row.UpdatedAt != nil {
		p.UpdatedAt = *row.UpdatedAt
	}
	if row.WindowTimeZone.Valid {
		w := &routing.Schedule{
			Start:    time.Duration(row.WindowStartMinute.Int32) * time.Minute,
			Duration: time.Duration(row.WindowMinutes.Int32) * time.Minute, TimeZone: row.WindowTimeZone.String,
		}
		for _, d := range row.WindowDays {
			w.Days = append(w.Days, time.Weekday(d))
		}
		p.Window = w
	}
	return updates.NamedPolicy{Policy: p, GroupName: row.Name}
}

// PutPolicy implements updates.PolicyStore.
func (s *UpdatePolicyStore) PutPolicy(ctx context.Context, scope tenant.Scope, group tenant.GroupID, p *updates.Policy, audit access.AuditEntry) error {
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		exists, err := q.NodeGroupExists(ctx, uuid.UUID(group))
		if err != nil {
			return wrap(err, "find group")
		}
		if !exists {
			return access.ErrNotFound
		}
		if p == nil {
			err = q.DeleteUpdatePolicy(ctx, uuid.UUID(group))
		} else {
			params := sqlcdb.UpsertUpdatePolicyParams{
				GroupID: uuid.UUID(group), Mode: int16(p.Mode), AutoReboot: p.AutoReboot,
				MaxParallel: int32(p.Rollout.MaxParallel), StopOnFailure: p.Rollout.StopOnFailure, //nolint:gosec // validated: 1 to 50
				HealthCheckMinutes: int32(p.Rollout.HealthCheck / time.Minute), UpdatedAt: p.UpdatedAt, UpdatedBy: optionalUser(p.UpdatedBy), //nolint:gosec // validated: 5 to 120
			}
			if w := p.Window; w != nil {
				params.WindowStartMinute = pgtype.Int4{Int32: int32(w.Start / time.Minute), Valid: true} //nolint:gosec // validated: within a day
				params.WindowMinutes = pgtype.Int4{Int32: int32(w.Duration / time.Minute), Valid: true}  //nolint:gosec // validated: at most a day
				params.WindowTimeZone = pgtype.Text{String: w.TimeZone, Valid: true}
				for _, d := range w.Days {
					params.WindowDays = append(params.WindowDays, int16(d)) //nolint:gosec // a weekday
				}
			}
			err = q.UpsertUpdatePolicy(ctx, params)
		}
		if err != nil {
			return wrap(err, "store update policy")
		}
		return appendAudit(ctx, q, audit)
	})
}
