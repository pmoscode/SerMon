package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/organization"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// OrganizationLifecycleStore persists locks and deletions of organizations (M2-33).
type OrganizationLifecycleStore struct{ db *DB }

var _ organization.LifecycleStore = (*OrganizationLifecycleStore)(nil)

// NewOrganizationLifecycleStore returns the store.
func NewOrganizationLifecycleStore(db *DB) *OrganizationLifecycleStore {
	return &OrganizationLifecycleStore{db: db}
}

func (s *OrganizationLifecycleStore) inOrg(ctx context.Context, org tenant.OrgID, fn func(q *sqlcdb.Queries) error) error {
	scope, err := tenant.ForAllGroups(org)
	if err != nil {
		return access.ErrNotFound
	}
	return s.db.InTenant(ctx, scope, fn)
}

// State implements organization.LifecycleStore.
func (s *OrganizationLifecycleStore) State(ctx context.Context, org tenant.OrgID) (organization.State, error) {
	var st organization.State
	err := s.inOrg(ctx, org, func(q *sqlcdb.Queries) error {
		r, err := q.OrganizationState(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return access.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("load organization: %w", err)
		}
		st = organization.State{Name: r.Name, LockedAt: r.LockedAt, DeletionDueAt: r.DeletionDueAt}
		return nil
	})
	return st, err
}

// SetLock implements organization.LifecycleStore: lock and date, the sessions of a locked
// organization, the entry in its audit log and in the instance's – one transaction.
func (s *OrganizationLifecycleStore) SetLock(ctx context.Context, org tenant.OrgID, lockedAt, dueAt *time.Time, audit access.AuditEntry) error {
	return s.inOrg(ctx, org, func(q *sqlcdb.Queries) error {
		n, err := q.SetOrganizationLock(ctx, sqlcdb.SetOrganizationLockParams{LockedAt: lockedAt, DeletionDueAt: dueAt})
		if err != nil {
			return fmt.Errorf("set lock: %w", err)
		}
		if n == 0 {
			return access.ErrNotFound
		}
		if lockedAt != nil {
			id := uuid.UUID(org)
			if err := q.DeleteOrganizationSessions(ctx, &id); err != nil {
				return fmt.Errorf("end sessions: %w", err)
			}
		}
		if err := appendAudit(ctx, q, audit); err != nil {
			return err
		}
		details := map[string]any{}
		for k, v := range audit.Details {
			details[k] = v
		}
		actor := audit.Actor
		return appendInstanceAudit(ctx, q, &actor, audit.Action, audit.Target, details)
	})
}

// AdminEmails implements organization.LifecycleStore.
func (s *OrganizationLifecycleStore) AdminEmails(ctx context.Context, org tenant.OrgID) ([]string, error) {
	var out []string
	err := s.inOrg(ctx, org, func(q *sqlcdb.Queries) error {
		var err error
		out, err = q.OrganizationAdminEmails(ctx)
		return wrap(err, "load admins")
	})
	return out, err
}

// ExportAudit implements organization.LifecycleStore: the chain is checked as VerifyAuditChain
// does, and the export is recorded in the instance's log.
func (s *OrganizationLifecycleStore) ExportAudit(ctx context.Context, org tenant.OrgID, by identity.UserID) ([]organization.ExportedEntry, bool, error) {
	var out []organization.ExportedEntry
	valid := true
	err := s.inOrg(ctx, org, func(q *sqlcdb.Queries) error {
		entries, err := q.AuditChain(ctx)
		if err != nil {
			return fmt.Errorf("load audit log: %w", err)
		}
		var prev []byte
		prev, valid = chainStart(orgLinks(entries))
		for i, e := range entries {
			if e.Seq != entries[0].Seq+int64(i) || !bytes.Equal(e.PrevHash, prev) || !bytes.Equal(e.Hash, auditHash(e)) {
				valid = false
			}
			prev = e.Hash
			x := organization.ExportedEntry{
				Seq: e.Seq, At: e.At, Action: e.Action, Target: e.Target, Details: json.RawMessage(e.Details),
				PrevHash: e.PrevHash, Hash: e.Hash,
			}
			if e.ActorID != nil {
				actor := identity.UserID(*e.ActorID)
				x.Actor = &actor
			}
			out = append(out, x)
		}
		return appendInstanceAudit(ctx, q, &by, "org.audit_export", "org:"+org.String(),
			map[string]any{"entries": len(entries), "chain_valid": valid})
	})
	return out, valid, err
}

// Purge implements organization.LifecycleStore: org_purge refuses what is not locked and due.
func (s *OrganizationLifecycleStore) Purge(ctx context.Context, org tenant.OrgID, now time.Time, by *identity.UserID, name string) (int64, error) {
	var accounts int64
	err := s.db.instanceTx(ctx, func(q *sqlcdb.Queries) error {
		var err error
		if accounts, err = q.PurgeOrganization(ctx, sqlcdb.PurgeOrganizationParams{OrgID: uuid.UUID(org), At: now}); err != nil {
			return fmt.Errorf("purge: %w", err)
		}
		return appendInstanceAudit(ctx, q, by, "org.delete", "org:"+org.String(), map[string]any{"name": name, "accounts_deleted": accounts})
	})
	return accounts, err
}

// lockedOrganizations returns the locked organizations (definer function, no tenant data).
func (s *OrganizationLifecycleStore) lockedOrganizations(ctx context.Context) ([]tenant.OrgID, error) {
	rows, err := s.db.withoutTenant().AdminListOrganizations(ctx)
	if err != nil {
		return nil, fmt.Errorf("list organizations: %w", err)
	}
	var out []tenant.OrgID
	for _, r := range rows {
		if r.Locked {
			out = append(out, tenant.OrgID(r.ID))
		}
	}
	return out, nil
}

// ClaimReminders implements organization.LifecycleStore.
func (s *OrganizationLifecycleStore) ClaimReminders(ctx context.Context, before time.Time) ([]organization.Reminder, error) {
	orgs, err := s.lockedOrganizations(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var out []organization.Reminder
	for _, org := range orgs {
		err := s.inOrg(ctx, org, func(q *sqlcdb.Queries) error {
			rows, err := q.ClaimDeletionReminder(ctx, sqlcdb.ClaimDeletionReminderParams{Now: &now, Before: &before})
			if err != nil {
				return fmt.Errorf("claim reminder: %w", err)
			}
			for _, r := range rows {
				if r.DeletionDueAt != nil {
					out = append(out, organization.Reminder{Org: org, Name: r.Name, DueAt: *r.DeletionDueAt})
				}
			}
			return nil
		})
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// DueForDeletion implements organization.LifecycleStore.
func (s *OrganizationLifecycleStore) DueForDeletion(ctx context.Context, now time.Time) ([]tenant.OrgID, error) {
	orgs, err := s.lockedOrganizations(ctx)
	if err != nil {
		return nil, err
	}
	var out []tenant.OrgID
	for _, org := range orgs {
		st, err := s.State(ctx, org)
		if errors.Is(err, access.ErrNotFound) {
			continue // deleted meanwhile
		}
		if err != nil {
			return out, err
		}
		if st.DeletionDueAt != nil && !st.DeletionDueAt.After(now) {
			out = append(out, org)
		}
	}
	return out, nil
}
