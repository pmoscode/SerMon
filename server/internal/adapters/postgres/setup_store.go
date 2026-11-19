package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/setup"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// SetupStore persists the first installation (instance-level tables, ADR 0007).
type SetupStore struct{ db *DB }

var _ setup.Store = (*SetupStore)(nil)

// NewSetupStore returns the PostgreSQL setup store.
func NewSetupStore(db *DB) *SetupStore { return &SetupStore{db: db} }

// Completed reports whether the setup was completed.
func (s *SetupStore) Completed(ctx context.Context) (bool, error) {
	done, err := s.db.withoutTenant().SetupCompleted(ctx)
	return done, wrap(err, "load setup state")
}

// IssueToken stores the hash of a new setup token, replacing a previous one.
func (s *SetupStore) IssueToken(ctx context.Context, hash [32]byte, expires time.Time) error {
	n, err := s.db.withoutTenant().IssueSetupToken(ctx, sqlcdb.IssueSetupTokenParams{TokenHash: hash[:], ExpiresAt: &expires})
	if err != nil {
		return fmt.Errorf("issue setup token: %w", err)
	}
	if n == 0 {
		return setup.ErrAlreadyInitialized
	}
	return nil
}

// Complete claims the token and creates the first superadmin, organization, membership and
// org-wide admin binding in one transaction (nothing is created when the token is invalid).
func (s *SetupStore) Complete(ctx context.Context, hash [32]byte, now time.Time, a setup.FirstAdmin) error {
	scope, err := tenant.ForAllGroups(a.Org)
	if err != nil {
		return fmt.Errorf("setup organization: %w", err)
	}
	user, org := uuid.UUID(a.User), uuid.UUID(a.Org)
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		n, err := q.ClaimSetupToken(ctx, sqlcdb.ClaimSetupTokenParams{TokenHash: hash[:], Now: &now})
		if err != nil {
			return fmt.Errorf("claim setup token: %w", err)
		}
		if n == 0 {
			return setup.ErrInvalidToken
		}
		steps := []struct {
			op  string
			run func() error
		}{
			{"create organization", func() error {
				_, err := q.CreateOrganization(ctx, sqlcdb.CreateOrganizationParams{ID: org, Name: a.OrganizationName})
				return wrap(err, "insert")
			}},
			{"create user", func() error {
				return q.CreateUser(ctx, sqlcdb.CreateUserParams{ID: user, Email: a.Email, DisplayName: a.DisplayName})
			}},
			{"set password", func() error {
				return q.SetPassword(ctx, sqlcdb.SetPasswordParams{UserID: user, PasswordHash: a.PasswordHash, ChangedAt: now})
			}},
			{"add membership", func() error { return q.AddMembership(ctx, sqlcdb.AddMembershipParams{OrgID: org, UserID: user}) }},
			{"grant admin", func() error {
				return q.CreateRoleBinding(ctx, sqlcdb.CreateRoleBindingParams{ID: a.Binding, UserID: user, Role: role.Admin.String()})
			}},
			{"add superadmin", func() error { return q.AddSuperadmin(ctx, user) }},
		}
		for _, step := range steps {
			if err := step.run(); err != nil {
				return fmt.Errorf("%s: %w", step.op, err)
			}
		}
		return appendAudit(ctx, q, access.AuditEntry{
			Actor: a.User, Action: "instance.setup", Target: "org:" + a.Org.String(),
			Details: map[string]string{"organization": a.OrganizationName},
		})
	})
}

// IsSuperadmin reports whether the user is a superadmin (instance-level table, no tenant).
func (s *AccessStore) IsSuperadmin(ctx context.Context, user identity.UserID) (bool, error) {
	super, err := s.db.withoutTenant().IsSuperadmin(ctx, uuid.UUID(user))
	return super, wrap(err, "check superadmin")
}
