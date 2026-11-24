package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/auth"
	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// SessionStore implements session.Store (instance-level table, looked up by token hash).
type SessionStore struct{ db *DB }

var _ session.Store = (*SessionStore)(nil)

// NewSessionStore returns the PostgreSQL session store.
func NewSessionStore(db *DB) *SessionStore { return &SessionStore{db: db} }

// Insert stores a new session.
func (s *SessionStore) Insert(ctx context.Context, se session.Session) error {
	err := s.db.withoutTenant().InsertSession(ctx, sqlcdb.InsertSessionParams{
		ID: se.ID, UserID: uuid.UUID(se.User), OrgID: orgPtr(se.Org),
		CreatedAt: se.CreatedAt, LastSeenAt: se.LastSeenAt, StepUpAt: se.StepUpAt,
		SecondFactorPending: se.SecondFactorPending, ExternalMfa: se.ExternalMFA,
	})
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return nil
}

// Get loads a session by its ID (token hash).
func (s *SessionStore) Get(ctx context.Context, id string) (session.Session, bool, error) {
	row, err := s.db.withoutTenant().GetSession(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return session.Session{}, false, nil
	}
	if err != nil {
		return session.Session{}, false, fmt.Errorf("get session: %w", err)
	}
	se := session.Session{
		ID: row.ID, User: identity.UserID(row.UserID),
		CreatedAt: row.CreatedAt, LastSeenAt: row.LastSeenAt, StepUpAt: row.StepUpAt,
		SecondFactorPending: row.SecondFactorPending, ExternalMFA: row.ExternalMfa,
	}
	if row.OrgID != nil {
		se.Org = tenant.OrgID(*row.OrgID)
	}
	return se, true, nil
}

// Touch renews the last activity.
func (s *SessionStore) Touch(ctx context.Context, id string, at time.Time) error {
	if err := s.db.withoutTenant().TouchSession(ctx, sqlcdb.TouchSessionParams{ID: id, At: at}); err != nil {
		return fmt.Errorf("touch session: %w", err)
	}
	return nil
}

// SetStepUp records a re-authentication.
func (s *SessionStore) SetStepUp(ctx context.Context, id string, at time.Time) error {
	if err := s.db.withoutTenant().SetSessionStepUp(ctx, sqlcdb.SetSessionStepUpParams{ID: id, At: at}); err != nil {
		return fmt.Errorf("set step-up: %w", err)
	}
	return nil
}

// Delete removes a session.
func (s *SessionStore) Delete(ctx context.Context, id string) error {
	if err := s.db.withoutTenant().DeleteSession(ctx, id); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// DeleteForUser removes the sessions of a user but keep.
func (s *SessionStore) DeleteForUser(ctx context.Context, user identity.UserID, keep string) error {
	if err := s.db.withoutTenant().DeleteUserSessions(ctx, sqlcdb.DeleteUserSessionsParams{UserID: uuid.UUID(user), Keep: keep}); err != nil {
		return fmt.Errorf("delete user sessions: %w", err)
	}
	return nil
}

// CredentialStore persists password credentials and answers tenant-less login lookups.
type CredentialStore struct{ db *DB }

var _ auth.CredentialRepository = (*CredentialStore)(nil)

// NewCredentialStore returns the PostgreSQL credential store.
func NewCredentialStore(db *DB) *CredentialStore { return &CredentialStore{db: db} }

// FindUserByEmail returns the user with the e-mail address (case-insensitive).
func (s *CredentialStore) FindUserByEmail(ctx context.Context, email string) (identity.UserID, bool, error) {
	id, err := s.db.withoutTenant().FindUserByEmail(ctx, email)
	if err != nil {
		return identity.UserID{}, false, fmt.Errorf("find user: %w", err)
	}
	if id == uuid.Nil {
		return identity.UserID{}, false, nil
	}
	return identity.UserID(id), true, nil
}

// Organizations returns the organizations the user is a member of.
func (s *CredentialStore) Organizations(ctx context.Context, user identity.UserID) ([]auth.Organization, error) {
	rows, err := s.db.withoutTenant().UserOrganizations(ctx, uuid.UUID(user))
	if err != nil {
		return nil, fmt.Errorf("list user organizations: %w", err)
	}
	orgs := make([]auth.Organization, 0, len(rows))
	for _, r := range rows {
		orgs = append(orgs, auth.Organization{ID: tenant.OrgID(r.OrgID), Name: r.Name})
	}
	return orgs, nil
}

// Get returns the credentials of a user.
func (s *CredentialStore) Get(ctx context.Context, user identity.UserID) (auth.Credentials, bool, error) {
	row, err := s.db.withoutTenant().GetCredentials(ctx, uuid.UUID(user))
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.Credentials{}, false, nil
	}
	if err != nil {
		return auth.Credentials{}, false, fmt.Errorf("get credentials: %w", err)
	}
	return auth.Credentials{User: user, PasswordHash: row.PasswordHash, FailedAttempts: row.FailedAttempts, LockedUntil: row.LockedUntil}, true, nil
}

// SetPassword sets a new password hash and clears failed attempts and locks.
func (s *CredentialStore) SetPassword(ctx context.Context, user identity.UserID, hash string, at time.Time) error {
	err := s.db.withoutTenant().SetPassword(ctx, sqlcdb.SetPasswordParams{UserID: uuid.UUID(user), PasswordHash: hash, ChangedAt: at})
	if err != nil {
		return fmt.Errorf("set password: %w", err)
	}
	return nil
}

// UpdatePasswordHash replaces the hash (rehash with current parameters after a login).
func (s *CredentialStore) UpdatePasswordHash(ctx context.Context, user identity.UserID, hash string) error {
	err := s.db.withoutTenant().UpdatePasswordHash(ctx, sqlcdb.UpdatePasswordHashParams{UserID: uuid.UUID(user), PasswordHash: hash})
	if err != nil {
		return fmt.Errorf("update password hash: %w", err)
	}
	return nil
}

// ReserveAttempt counts an attempt before it is checked (compare-and-swap, see auth.CredentialRepository).
func (s *CredentialStore) ReserveAttempt(ctx context.Context, user identity.UserID, seen int32, lockedUntil *time.Time, now time.Time) (bool, error) {
	rows, err := s.db.withoutTenant().ReserveAttempt(ctx, sqlcdb.ReserveAttemptParams{
		UserID: uuid.UUID(user), Seen: seen, LockedUntil: lockedUntil, Now: &now,
	})
	if err != nil {
		return false, fmt.Errorf("reserve login attempt: %w", err)
	}
	return rows == 1, nil
}

// ReleaseAttempt takes a reserved attempt back.
func (s *CredentialStore) ReleaseAttempt(ctx context.Context, user identity.UserID, lockAfter int32) error {
	if err := s.db.withoutTenant().ReleaseAttempt(ctx, sqlcdb.ReleaseAttemptParams{UserID: uuid.UUID(user), LockAfter: lockAfter}); err != nil {
		return fmt.Errorf("release login attempt: %w", err)
	}
	return nil
}

// ResetFailedLogins clears the failure counter after a successful login.
func (s *CredentialStore) ResetFailedLogins(ctx context.Context, user identity.UserID) error {
	if err := s.db.withoutTenant().ResetFailedLogins(ctx, uuid.UUID(user)); err != nil {
		return fmt.Errorf("reset failed logins: %w", err)
	}
	return nil
}

func orgPtr(org tenant.OrgID) *uuid.UUID {
	if uuid.UUID(org) == uuid.Nil {
		return nil
	}
	id := uuid.UUID(org)
	return &id
}

// Profile returns e-mail address and display name of a user (tenant-less, for the session view).
func (s *CredentialStore) Profile(ctx context.Context, user identity.UserID) (auth.Profile, bool, error) {
	row, err := s.db.withoutTenant().UserProfile(ctx, uuid.UUID(user))
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.Profile{}, false, nil
	}
	if err != nil {
		return auth.Profile{}, false, fmt.Errorf("get user profile: %w", err)
	}
	return auth.Profile{Email: row.Email, DisplayName: row.DisplayName}, true, nil
}

// HasPassword tells whether the user has a password (accounts from OIDC have none, M3-22).
func (s *CredentialStore) HasPassword(ctx context.Context, user identity.UserID) (bool, error) {
	ok, err := s.db.withoutTenant().HasPassword(ctx, uuid.UUID(user))
	if err != nil {
		return false, fmt.Errorf("has password: %w", err)
	}
	return ok, nil
}
