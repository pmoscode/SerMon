package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/quota"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var _ access.Store = (*AccessStore)(nil)

// ListGroups returns the node groups of the scope (all groups or the scope's groups).
func (s *AccessStore) ListGroups(ctx context.Context, scope tenant.Scope) ([]access.Group, error) {
	var groups []access.Group
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		rows, err := q.ListNodeGroups(ctx, sqlcdb.ListNodeGroupsParams{AllGroups: scope.AllGroups(), GroupIds: groupIDs(scope)})
		for _, r := range rows {
			groups = append(groups, access.Group{ID: tenant.GroupID(r.ID), Name: r.Name, CreatedAt: r.CreatedAt})
		}
		return wrap(err, "list node groups")
	})
	return groups, err
}

// CreateGroup creates a node group (ErrConflict for a duplicate name).
func (s *AccessStore) CreateGroup(ctx context.Context, scope tenant.Scope, g access.Group, audit access.AuditEntry) error {
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		if _, err := q.CreateNodeGroup(ctx, sqlcdb.CreateNodeGroupParams{ID: uuid.UUID(g.ID), Name: g.Name}); err != nil {
			return accessError(err, "create node group")
		}
		return appendAudit(ctx, q, audit)
	})
}

// RenameGroup renames a node group of the organization.
func (s *AccessStore) RenameGroup(ctx context.Context, scope tenant.Scope, id tenant.GroupID, name string, audit access.AuditEntry) error {
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		n, err := q.RenameNodeGroup(ctx, sqlcdb.RenameNodeGroupParams{ID: uuid.UUID(id), Name: name})
		if err != nil {
			return accessError(err, "rename node group")
		}
		if n == 0 {
			return access.ErrNotFound
		}
		return appendAudit(ctx, q, audit)
	})
}

// DeleteGroup deletes a node group of the organization (its role bindings are deleted with it).
func (s *AccessStore) DeleteGroup(ctx context.Context, scope tenant.Scope, id tenant.GroupID, audit access.AuditEntry) error {
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		n, err := q.DeleteNodeGroup(ctx, uuid.UUID(id))
		if err != nil {
			return accessError(err, "delete node group")
		}
		if n == 0 {
			return access.ErrNotFound
		}
		return appendAudit(ctx, q, audit)
	})
}

// ListMembers returns the organization's members with their role bindings.
func (s *AccessStore) ListMembers(ctx context.Context, scope tenant.Scope) ([]access.Member, error) {
	var members []access.Member
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		users, err := q.ListMembers(ctx)
		if err != nil {
			return fmt.Errorf("list members: %w", err)
		}
		bindings, err := q.ListRoleBindings(ctx)
		if err != nil {
			return fmt.Errorf("list role bindings: %w", err)
		}
		byUser := map[uuid.UUID][]access.RoleBinding{}
		for _, b := range bindings {
			rb, err := roleBinding(b.ID, b.GroupID, b.Role)
			if err != nil {
				return err
			}
			byUser[b.UserID] = append(byUser[b.UserID], rb)
		}
		for _, u := range users {
			members = append(members, access.Member{
				User: identity.UserID(u.ID), Email: u.Email, DisplayName: u.DisplayName, Bindings: byUser[u.ID],
			})
		}
		return nil
	})
	return members, err
}

// GrantRole creates or replaces the member's binding on the group.
func (s *AccessStore) GrantRole(ctx context.Context, scope tenant.Scope, g access.Grant, audit access.AuditEntry) (access.BindingID, error) {
	var id uuid.UUID
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		if err := q.LockAccessChanges(ctx); err != nil {
			return fmt.Errorf("lock access changes: %w", err)
		}
		var group *uuid.UUID
		if g.Group != nil {
			gid := uuid.UUID(*g.Group)
			group = &gid
		}
		var err error
		id, err = q.UpsertRoleBinding(ctx, sqlcdb.UpsertRoleBindingParams{
			ID: uuid.UUID(g.ID), UserID: uuid.UUID(g.User), GroupID: group, Role: g.Role.String(),
		})
		if err != nil {
			return accessError(err, "grant role")
		}
		if err := requireOrgAdmin(ctx, q); err != nil {
			return err
		}
		return appendAudit(ctx, q, audit)
	})
	return access.BindingID(id), err
}

// RevokeRole deletes a role binding; the audit entry gets the binding's user, group and role.
func (s *AccessStore) RevokeRole(ctx context.Context, scope tenant.Scope, id access.BindingID, audit access.AuditEntry) error {
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		if err := q.LockAccessChanges(ctx); err != nil {
			return fmt.Errorf("lock access changes: %w", err)
		}
		b, err := q.GetRoleBinding(ctx, uuid.UUID(id))
		if err != nil {
			return accessError(err, "load role binding")
		}
		if _, err := q.DeleteRoleBinding(ctx, b.ID); err != nil {
			return fmt.Errorf("delete role binding: %w", err)
		}
		if err := requireOrgAdmin(ctx, q); err != nil {
			return err
		}
		group := "*"
		if b.GroupID != nil {
			group = b.GroupID.String()
		}
		audit.Details = withDetails(audit.Details, "user", b.UserID.String(), "group", group, "role", b.Role)
		return appendAudit(ctx, q, audit)
	})
}

// RemoveMember removes the membership, its role bindings and the member's sessions in the organization.
func (s *AccessStore) RemoveMember(ctx context.Context, scope tenant.Scope, user identity.UserID, audit access.AuditEntry) error {
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		if err := q.LockAccessChanges(ctx); err != nil {
			return fmt.Errorf("lock access changes: %w", err)
		}
		n, err := q.DeleteMembership(ctx, uuid.UUID(user))
		if err != nil {
			return fmt.Errorf("delete membership: %w", err)
		}
		if n == 0 {
			return access.ErrNotFound
		}
		if err := q.DeleteOrgSessionsOfUser(ctx, uuid.UUID(user)); err != nil {
			return fmt.Errorf("end sessions: %w", err)
		}
		if err := requireOrgAdmin(ctx, q); err != nil {
			return err
		}
		return appendAudit(ctx, q, audit)
	})
}

// ListAudit returns entries with seq < beforeSeq (0 = newest), newest first.
func (s *AccessStore) ListAudit(ctx context.Context, scope tenant.Scope, beforeSeq int64, limit int) ([]access.AuditRecord, error) {
	var records []access.AuditRecord
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		rows, err := q.ListAudit(ctx, sqlcdb.ListAuditParams{BeforeSeq: beforeSeq, MaxRows: int32(min(limit, 1000))}) //nolint:gosec // G115: bounded by min
		if err != nil {
			return fmt.Errorf("list audit log: %w", err)
		}
		for _, r := range rows {
			rec := access.AuditRecord{Seq: r.Seq, At: r.At, Action: r.Action, Target: r.Target, Details: detailStrings(r.Details)}
			if r.ActorID != nil {
				actor := identity.UserID(*r.ActorID)
				rec.Actor = &actor
			}
			records = append(records, rec)
		}
		return nil
	})
	return records, err
}

// requireOrgAdmin fails the transaction when no org-wide admin is left.
func requireOrgAdmin(ctx context.Context, q *sqlcdb.Queries) error {
	n, err := q.CountOrgAdmins(ctx)
	if err != nil {
		return fmt.Errorf("count org admins: %w", err)
	}
	if n == 0 {
		return access.ErrLastAdmin
	}
	return nil
}

func appendAudit(ctx context.Context, q *sqlcdb.Queries, e access.AuditEntry) error {
	details := e.Details
	if details == nil {
		details = map[string]string{}
	}
	payload, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("encode audit details: %w", err)
	}
	var actor *uuid.UUID
	if !e.Actor.IsZero() {
		id := uuid.UUID(e.Actor)
		actor = &id
	}
	_, err = q.AppendAudit(ctx, sqlcdb.AppendAuditParams{ActorID: actor, Action: e.Action, Target: e.Target, Details: payload})
	return wrap(err, "append audit entry")
}

// accessError maps constraint violations to the access errors (foreign or unknown objects
// look the same: ErrNotFound).
func accessError(err error, op string) error {
	var pgErr *pgconn.PgError
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return access.ErrNotFound
	case errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.TableName == "node_groups":
		return access.ErrConflict
	case errors.As(err, &pgErr) && pgErr.Code == "23503" && pgErr.TableName == "role_bindings":
		return access.ErrNotFound
	}
	return fmt.Errorf("%s: %w", op, err)
}

func roleBinding(id uuid.UUID, group *uuid.UUID, name string) (access.RoleBinding, error) {
	r, err := role.Parse(name)
	if err != nil {
		return access.RoleBinding{}, fmt.Errorf("role binding %s: %w", id, err)
	}
	b := access.RoleBinding{ID: access.BindingID(id), Role: r}
	if group != nil {
		g := tenant.GroupID(*group)
		b.Group = &g
	}
	return b, nil
}

func withDetails(details map[string]string, kv ...string) map[string]string {
	out := map[string]string{}
	for k, v := range details {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = kv[i+1]
	}
	return out
}

// detailStrings decodes audit details; non-string values are rendered as JSON.
func detailStrings(raw []byte) map[string]string {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(values))
	for k, v := range values {
		var str string
		if json.Unmarshal(v, &str) == nil {
			out[k] = str
		} else {
			out[k] = string(v)
		}
	}
	return out
}

// QuotaUsage returns the organization's quotas and its nodes, members and open invitations.
func (s *AccessStore) QuotaUsage(ctx context.Context, scope tenant.Scope, now time.Time) (access.QuotaUsage, error) {
	var u access.QuotaUsage
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		row, err := q.OrganizationQuotas(ctx)
		if err != nil {
			return fmt.Errorf("load quotas: %w", err)
		}
		u.Quotas = quota.Quotas{
			MaxNodes: quotaOf(row.MaxNodes), MaxMembers: quotaOf(row.MaxMembers), RetentionCapDays: quotaOf(row.RetentionCapDays),
			SamplesPerSecond: quotaOf(row.SamplesPerSecond), SeriesPerNode: quotaOf(row.SeriesPerNode),
		}
		if u.Nodes, err = q.CountNodes(ctx); err != nil {
			return fmt.Errorf("count nodes: %w", err)
		}
		if u.Members, err = q.CountMembers(ctx); err != nil {
			return fmt.Errorf("count members: %w", err)
		}
		if u.OpenInvitations, err = q.CountOpenInvitations(ctx, sqlcdb.CountOpenInvitationsParams{Now: now, Email: ""}); err != nil {
			return fmt.Errorf("count invitations: %w", err)
		}
		return nil
	})
	if err != nil {
		return access.QuotaUsage{}, fmt.Errorf("quota usage: %w", err)
	}
	return u, nil
}
