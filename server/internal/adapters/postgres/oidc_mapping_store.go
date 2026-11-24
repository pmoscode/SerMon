package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/auth/oidc"
	"github.com/pmoscode/SerMon/server/internal/app/oidcmapping"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/quota"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// OIDCMappingStore keeps the OIDC group mappings and applies them at sign-ins (M3-21, migration
// 00044).
type OIDCMappingStore struct{ db *DB }

var _ oidcmapping.Store = (*OIDCMappingStore)(nil)

// NewOIDCMappingStore returns the store.
func NewOIDCMappingStore(db *DB) *OIDCMappingStore { return &OIDCMappingStore{db: db} }

func groupUUID(g *tenant.GroupID) *uuid.UUID {
	if g == nil {
		return nil
	}
	id := uuid.UUID(*g)
	return &id
}

func groupDetail(g *uuid.UUID) string {
	if g == nil {
		return "*"
	}
	return g.String()
}

func listMappings(ctx context.Context, q *sqlcdb.Queries) ([]oidcmapping.Mapping, error) {
	rows, err := q.ListOIDCMappings(ctx)
	if err != nil {
		return nil, fmt.Errorf("list oidc mappings: %w", err)
	}
	out := make([]oidcmapping.Mapping, 0, len(rows))
	for _, r := range rows {
		parsed, err := role.Parse(r.Role)
		if err != nil {
			return nil, fmt.Errorf("oidc mapping %s: %w", r.ID, err)
		}
		out = append(out, oidcmapping.Mapping{ID: oidcmapping.ID(r.ID), OIDCGroup: r.OidcGroup, Group: groupID(r.GroupID), Role: parsed, CreatedAt: r.CreatedAt})
	}
	return out, nil
}

// List implements oidcmapping.Store.
func (s *OIDCMappingStore) List(ctx context.Context, scope tenant.Scope) ([]oidcmapping.Mapping, error) {
	var out []oidcmapping.Mapping
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		var err error
		out, err = listMappings(ctx, q)
		return err
	})
	return out, err
}

// Create implements oidcmapping.Store: ErrExists for a duplicate, ErrTooMany at the bound,
// access.ErrNotFound for a node group of no one.
func (s *OIDCMappingStore) Create(ctx context.Context, scope tenant.Scope, m oidcmapping.Mapping, audit access.AuditEntry) error {
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		if err := q.LockAccessChanges(ctx); err != nil {
			return fmt.Errorf("lock access changes: %w", err)
		}
		n, err := q.CountOIDCMappings(ctx)
		if err != nil {
			return fmt.Errorf("count oidc mappings: %w", err)
		}
		if n >= oidcmapping.MaxMappings {
			return oidcmapping.ErrTooMany
		}
		err = q.CreateOIDCMapping(ctx, sqlcdb.CreateOIDCMappingParams{
			ID: uuid.UUID(m.ID), OidcGroup: m.OIDCGroup, GroupID: groupUUID(m.Group), Role: m.Role.String(), CreatedAt: m.CreatedAt,
		})
		var pgErr *pgconn.PgError
		switch {
		case errors.As(err, &pgErr) && pgErr.Code == "23505":
			return oidcmapping.ErrExists
		case errors.As(err, &pgErr) && pgErr.Code == "23503":
			return access.ErrNotFound
		case err != nil:
			return fmt.Errorf("create oidc mapping: %w", err)
		}
		return appendAudit(ctx, q, audit)
	})
}

// Delete implements oidcmapping.Store; the audit entry gets what the mapping gave.
func (s *OIDCMappingStore) Delete(ctx context.Context, scope tenant.Scope, id oidcmapping.ID, audit access.AuditEntry) error {
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		if err := q.LockAccessChanges(ctx); err != nil {
			return fmt.Errorf("lock access changes: %w", err)
		}
		r, err := q.DeleteOIDCMapping(ctx, uuid.UUID(id))
		if errors.Is(err, pgx.ErrNoRows) {
			return access.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("delete oidc mapping: %w", err)
		}
		audit.Details = withDetails(audit.Details, "oidc_group", r.OidcGroup, "group", groupDetail(r.GroupID), "role", r.Role)
		return appendAudit(ctx, q, audit)
	})
}

// MappedOrganizations implements oidcmapping.Store (SECURITY DEFINER, only IDs).
func (s *OIDCMappingStore) MappedOrganizations(ctx context.Context, groups []string) ([]tenant.OrgID, error) {
	if len(groups) == 0 {
		return nil, nil
	}
	ids, err := s.db.withoutTenant().OIDCMappedOrganizations(ctx, groups)
	if err != nil {
		return nil, fmt.Errorf("mapped organizations: %w", err)
	}
	out := make([]tenant.OrgID, len(ids))
	for i, id := range ids {
		out[i] = tenant.OrgID(id)
	}
	return out, nil
}

// Reconcile implements oidcmapping.Store: in one transaction of the organization, serialized
// with every other change of rights.
func (s *OIDCMappingStore) Reconcile(ctx context.Context, scope tenant.Scope, user identity.UserID, join bool,
	plan func([]oidcmapping.Mapping, []oidcmapping.Binding) []oidcmapping.Change,
) (oidcmapping.OrgResult, error) {
	var res oidcmapping.OrgResult
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		res = oidcmapping.OrgResult{}
		if err := q.LockAccessChanges(ctx); err != nil {
			return fmt.Errorf("lock access changes: %w", err)
		}
		locked, err := q.OrganizationLocked(ctx)
		if err != nil {
			return fmt.Errorf("organization lock: %w", err)
		}
		if locked {
			res.Skipped = "locked"
			return nil
		}
		mappings, err := listMappings(ctx, q)
		if err != nil {
			return err
		}
		member, err := q.IsMember(ctx, uuid.UUID(user))
		if err != nil {
			return fmt.Errorf("check membership: %w", err)
		}
		rows, err := q.RoleBindingsWithOrigin(ctx, uuid.UUID(user))
		if err != nil {
			return fmt.Errorf("role bindings: %w", err)
		}
		existing := make([]oidcmapping.Binding, 0, len(rows))
		byID := make(map[uuid.UUID]oidcmapping.Binding, len(rows))
		for _, r := range rows {
			parsed, err := role.Parse(r.Role)
			if err != nil {
				return fmt.Errorf("role binding %s: %w", r.ID, err)
			}
			b := oidcmapping.Binding{ID: r.ID, Group: groupID(r.GroupID), Role: parsed, FromOIDC: r.Origin == "oidc"}
			existing = append(existing, b)
			byID[b.ID] = b
		}
		changes := plan(mappings, existing)
		if !member {
			if !join || !hasAdd(changes) {
				res.Skipped = "not_member"
				return nil
			}
			if err := admitMember(ctx, q, user); err != nil {
				if errors.As(err, new(quota.Exceeded)) {
					res.Skipped = "quota"
					return nil
				}
				return err
			}
			if err := q.AddMembershipOfCurrentOrg(ctx, uuid.UUID(user)); err != nil {
				return fmt.Errorf("add membership: %w", err)
			}
			if err := appendAudit(ctx, q, access.AuditEntry{
				Action: "member.join", Target: "user:" + user.String(),
				Details: map[string]string{"origin": "oidc"},
			}); err != nil {
				return err
			}
			res.Joined = true
		}
		for _, c := range changes {
			applied, err := applyChange(ctx, q, user, c, byID[c.Binding])
			if err != nil {
				return err
			}
			if !applied {
				res.Kept++
				continue
			}
			res.Applied = append(res.Applied, c)
		}
		return nil
	})
	return res, err
}

func hasAdd(changes []oidcmapping.Change) bool {
	for _, c := range changes {
		if c.Kind == oidcmapping.Add {
			return true
		}
	}
	return false
}

// applyChange applies one change with its audit entry; false if it would remove the last
// org-wide admin (the organization keeps it).
func applyChange(ctx context.Context, q *sqlcdb.Queries, user identity.UserID, c oidcmapping.Change, old oidcmapping.Binding) (bool, error) {
	lastAdmin := func() (bool, error) {
		if old.Group != nil || old.Role != role.Admin {
			return false, nil
		}
		n, err := q.CountOrgAdmins(ctx)
		if err != nil {
			return false, fmt.Errorf("count org admins: %w", err)
		}
		return n <= 1, nil
	}
	group := groupDetail(groupUUID(c.Group))
	audit := access.AuditEntry{Target: "user:" + user.String(), Details: map[string]string{"origin": "oidc", "group": group}}
	switch c.Kind {
	case oidcmapping.Add:
		if err := q.CreateOIDCRoleBinding(ctx, sqlcdb.CreateOIDCRoleBindingParams{
			ID: access.NewID(), UserID: uuid.UUID(user), GroupID: groupUUID(c.Group), Role: c.Role.String(),
		}); err != nil {
			return false, fmt.Errorf("add role binding: %w", err)
		}
		audit.Action, audit.Details["role"] = "role.grant", c.Role.String()
	case oidcmapping.Update:
		if c.Role != role.Admin {
			if last, err := lastAdmin(); err != nil || last {
				return false, err
			}
		}
		if _, err := q.UpdateOIDCRoleBinding(ctx, sqlcdb.UpdateOIDCRoleBindingParams{ID: c.Binding, Role: c.Role.String()}); err != nil {
			return false, fmt.Errorf("update role binding: %w", err)
		}
		audit.Action, audit.Details["role"], audit.Details["previous_role"] = "role.grant", c.Role.String(), old.Role.String()
	case oidcmapping.Remove:
		if last, err := lastAdmin(); err != nil || last {
			return false, err
		}
		if _, err := q.DeleteOIDCRoleBinding(ctx, c.Binding); err != nil {
			return false, fmt.Errorf("delete role binding: %w", err)
		}
		audit.Action, audit.Details["role"] = "role.revoke", old.Role.String()
	default:
		return false, fmt.Errorf("unknown change %d", c.Kind)
	}
	return true, appendAudit(ctx, q, audit)
}

// Provision implements oidcmapping.Store: an account without a password with the identity, in
// the instance audit log. ErrEmailTaken if an account has the e-mail.
func (s *OIDCMappingStore) Provision(ctx context.Context, email, name string, id oidc.Identity) (identity.UserID, error) {
	user := access.NewID()
	err := s.db.instanceTx(ctx, func(q *sqlcdb.Queries) error {
		existing, err := q.FindUserByEmail(ctx, email)
		if err != nil {
			return fmt.Errorf("check address: %w", err)
		}
		if existing != uuid.Nil {
			return oidcmapping.ErrEmailTaken
		}
		err = q.CreateUser(ctx, sqlcdb.CreateUserParams{ID: user, Email: email, DisplayName: name})
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return oidcmapping.ErrEmailTaken // created concurrently
		}
		if err != nil {
			return fmt.Errorf("create user: %w", err)
		}
		n, err := q.InsertIdentity(ctx, sqlcdb.InsertIdentityParams{Issuer: id.Issuer, Subject: id.Subject, UserID: user, Email: text(email)})
		if err != nil {
			return fmt.Errorf("link identity: %w", err)
		}
		if n == 0 {
			return oidc.ErrIdentityTaken // provisioned concurrently
		}
		u := identity.UserID(user)
		return appendInstanceAudit(ctx, q, nil, "oidc.user_provisioned", "user:"+u.String(),
			map[string]any{"issuer": id.Issuer, "subject": id.Subject})
	})
	if err != nil {
		return identity.UserID{}, err
	}
	return identity.UserID(user), nil
}
