package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/account"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// MaxExportedAuditEntries bounds the audit entries of an export per organization.
const MaxExportedAuditEntries = 10000

// AccountStore persists accounts for their owners (V1-17). The rows of users only change through
// the SECURITY DEFINER functions of 00075 and 00076, for the user the service names.
type AccountStore struct{ db *DB }

var _ account.Store = (*AccountStore)(nil)

// NewAccountStore returns the store.
func NewAccountStore(db *DB) *AccountStore { return &AccountStore{db: db} }

func (s *AccountStore) audit(ctx context.Context, q *sqlcdb.Queries, e access.AuditEntry) error {
	actor := e.Actor
	details := map[string]any{}
	for k, v := range e.Details {
		details[k] = v
	}
	return appendInstanceAudit(ctx, q, &actor, e.Action, e.Target, details)
}

// Profile implements account.Store.
func (s *AccountStore) Profile(ctx context.Context, user identity.UserID) (account.Profile, bool, error) {
	row, err := s.db.withoutTenant().AccountProfile(ctx, uuid.UUID(user))
	if errors.Is(err, pgx.ErrNoRows) {
		return account.Profile{}, false, nil
	}
	if err != nil {
		return account.Profile{}, false, fmt.Errorf("load account: %w", err)
	}
	return account.Profile{Email: row.Email, DisplayName: row.DisplayName, CreatedAt: row.CreatedAt}, true, nil
}

// SetDisplayName implements account.Store.
func (s *AccountStore) SetDisplayName(ctx context.Context, user identity.UserID, name string, audit access.AuditEntry) error {
	return s.db.instanceTx(ctx, func(q *sqlcdb.Queries) error {
		changed, err := q.AccountSetDisplayName(ctx, sqlcdb.AccountSetDisplayNameParams{UserID: uuid.UUID(user), DisplayName: name})
		if err != nil {
			return fmt.Errorf("set display name: %w", err)
		}
		if !changed {
			return access.ErrNotFound
		}
		return s.audit(ctx, q, audit)
	})
}

// SetEmail implements account.Store.
func (s *AccountStore) SetEmail(ctx context.Context, user identity.UserID, email string, audit access.AuditEntry) (bool, error) {
	var changed bool
	err := s.db.instanceTx(ctx, func(q *sqlcdb.Queries) error {
		var err error
		if changed, err = q.AccountSetEmail(ctx, sqlcdb.AccountSetEmailParams{UserID: uuid.UUID(user), Email: email}); err != nil || !changed {
			return wrap(err, "set e-mail")
		}
		if err := q.DeleteEmailChange(ctx, uuid.UUID(user)); err != nil {
			return fmt.Errorf("drop e-mail change: %w", err)
		}
		return s.audit(ctx, q, audit)
	})
	return changed, err
}

// PendingChange implements account.Store.
func (s *AccountStore) PendingChange(ctx context.Context, user identity.UserID) (account.Pending, bool, error) {
	row, err := s.db.withoutTenant().GetEmailChange(ctx, uuid.UUID(user))
	if errors.Is(err, pgx.ErrNoRows) {
		return account.Pending{}, false, nil
	}
	if err != nil {
		return account.Pending{}, false, fmt.Errorf("load e-mail change: %w", err)
	}
	p := account.Pending{NewEmail: row.NewEmail, Attempts: int(row.Attempts), ExpiresAt: row.ExpiresAt}
	copy(p.CodeHash[:], row.CodeHash)
	return p, true, nil
}

// SavePendingChange implements account.Store.
func (s *AccountStore) SavePendingChange(ctx context.Context, user identity.UserID, p account.Pending) error {
	return wrap(s.db.withoutTenant().UpsertEmailChange(ctx, sqlcdb.UpsertEmailChangeParams{
		UserID: uuid.UUID(user), NewEmail: p.NewEmail, CodeHash: p.CodeHash[:], ExpiresAt: p.ExpiresAt,
	}), "save e-mail change")
}

// CountAttempt implements account.Store.
func (s *AccountStore) CountAttempt(ctx context.Context, user identity.UserID) (int, error) {
	n, err := s.db.withoutTenant().CountEmailChangeAttempt(ctx, uuid.UUID(user))
	if errors.Is(err, pgx.ErrNoRows) {
		return account.MaxCodeAttempts + 1, nil
	}
	if err != nil {
		return 0, fmt.Errorf("count attempt: %w", err)
	}
	return int(n), nil
}

// DeletePendingChange implements account.Store.
func (s *AccountStore) DeletePendingChange(ctx context.Context, user identity.UserID) error {
	return wrap(s.db.withoutTenant().DeleteEmailChange(ctx, uuid.UUID(user)), "drop e-mail change")
}

// FindUser implements account.Store.
func (s *AccountStore) FindUser(ctx context.Context, email string) (identity.UserID, bool, error) {
	id, err := s.db.withoutTenant().FindUserByEmail(ctx, email)
	if err != nil {
		return identity.UserID{}, false, fmt.Errorf("find account: %w", err)
	}
	return identity.UserID(id), id != uuid.Nil, nil
}

// Delete implements account.Store: account_delete writes the entries of the organizations, the
// instance's entry follows in the same transaction.
func (s *AccountStore) Delete(ctx context.Context, user identity.UserID, audit access.AuditEntry) (int, error) {
	var n int32
	err := s.db.instanceTx(ctx, func(q *sqlcdb.Queries) error {
		actor := uuid.UUID(audit.Actor)
		var err error
		n, err = q.DeleteAccount(ctx, sqlcdb.DeleteAccountParams{UserID: uuid.UUID(user), ActorID: &actor})
		var pgErr *pgconn.PgError
		switch {
		case errors.As(err, &pgErr) && pgErr.Code == "P0001" && pgErr.Hint == "superadmin":
			return account.ErrSuperadmin
		case errors.As(err, &pgErr) && pgErr.Code == "P0001" && pgErr.Hint == "last_admin":
			return access.ErrLastAdmin
		case err != nil:
			return fmt.Errorf("delete account: %w", err)
		case n < 0:
			return access.ErrNotFound
		}
		e := audit
		e.Details = map[string]string{}
		for k, v := range audit.Details {
			e.Details[k] = v
		}
		e.Details["memberships"] = fmt.Sprint(n)
		return s.audit(ctx, q, e)
	})
	return int(n), err
}

// PurgeAccounts implements account.Store: each deleted account gets an entry in the instance's log.
func (s *AccountStore) PurgeAccounts(ctx context.Context, before time.Time, limit int) (int, error) {
	var n int
	err := s.db.instanceTx(ctx, func(q *sqlcdb.Queries) error {
		ids, err := q.PurgeAccounts(ctx, sqlcdb.PurgeAccountsParams{Before: before, MaxRows: int32(min(limit, 10000))}) //nolint:gosec // G115: bounded
		if err != nil {
			return fmt.Errorf("delete accounts: %w", err)
		}
		for _, id := range ids {
			details := map[string]any{"reason": "no membership"}
			if err := appendInstanceAudit(ctx, q, nil, "account.purge", "user:"+id.String(), details); err != nil {
				return err
			}
		}
		n = len(ids)
		return nil
	})
	return n, err
}

// PurgeInvitations implements account.Store.
func (s *AccountStore) PurgeInvitations(ctx context.Context, before time.Time) (int, error) {
	n, err := s.db.withoutTenant().PurgeExpiredInvitations(ctx, before)
	return int(n), wrap(err, "delete expired invitations")
}

// PurgeEmailChanges implements account.Store.
func (s *AccountStore) PurgeEmailChanges(ctx context.Context, before time.Time) (int, error) {
	n, err := s.db.withoutTenant().PurgeEmailChanges(ctx, before)
	return int(n), wrap(err, "delete expired e-mail changes")
}

// PruneAudit implements account.Store: per organization in its tenant, then the instance's log.
func (s *AccountStore) PruneAudit(ctx context.Context, before time.Time) (int, error) {
	// Locked organizations too: their entries age as well.
	orgs, err := s.db.withoutTenant().AdminListOrganizations(ctx)
	if err != nil {
		return 0, fmt.Errorf("list organizations: %w", err)
	}
	var total int
	var errs []error
	for _, o := range orgs {
		scope, err := tenant.ForAllGroups(tenant.OrgID(o.ID))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		var deleted int64
		err = s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
			rows, err := q.PruneOrgAudit(ctx, sqlcdb.PruneOrgAuditParams{OrgID: o.ID, Before: before})
			if err != nil || len(rows) == 0 {
				return wrap(err, "prune audit log")
			}
			deleted = rows[0].Deleted
			return appendAudit(ctx, q, access.AuditEntry{
				Action: PruneAction, Target: "audit", Details: pruneDetails(rows[0].ThroughSeq, rows[0].LastHash),
			})
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("organization %s: %w", o.ID, err))
			continue
		}
		total += int(deleted)
	}
	var deleted int64
	err = s.db.instanceTx(ctx, func(q *sqlcdb.Queries) error {
		rows, err := q.PruneInstanceAudit(ctx, before)
		if err != nil || len(rows) == 0 {
			return wrap(err, "prune instance audit log")
		}
		deleted = rows[0].Deleted
		details := map[string]any{}
		for k, v := range pruneDetails(rows[0].ThroughSeq, rows[0].LastHash) {
			details[k] = v
		}
		return appendInstanceAudit(ctx, q, nil, PruneAction, "audit", details)
	})
	if err != nil {
		errs = append(errs, err)
	} else {
		total += int(deleted)
	}
	return total, errors.Join(errs...)
}
