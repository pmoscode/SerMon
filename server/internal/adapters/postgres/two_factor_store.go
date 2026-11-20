package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/auth/twofactor"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// TwoFactorStore implements twofactor.Store (instance-level tables, migration 00015).
type TwoFactorStore struct{ db *DB }

var _ twofactor.Store = (*TwoFactorStore)(nil)

// NewTwoFactorStore returns the store.
func NewTwoFactorStore(db *DB) *TwoFactorStore { return &TwoFactorStore{db: db} }

// Get implements twofactor.Store.
func (s *TwoFactorStore) Get(ctx context.Context, user identity.UserID) (twofactor.Secret, bool, error) {
	row, err := s.db.withoutTenant().GetTOTP(ctx, uuid.UUID(user))
	if errors.Is(err, pgx.ErrNoRows) {
		return twofactor.Secret{}, false, nil
	}
	if err != nil {
		return twofactor.Secret{}, false, fmt.Errorf("get TOTP: %w", err)
	}
	return twofactor.Secret{Sealed: row.SecretSealed, Confirmed: row.Confirmed, LastStep: row.LastStep}, true, nil
}

// SavePending implements twofactor.Store.
func (s *TwoFactorStore) SavePending(ctx context.Context, user identity.UserID, sealed []byte) error {
	rows, err := s.db.withoutTenant().SavePendingTOTP(ctx, sqlcdb.SavePendingTOTPParams{UserID: uuid.UUID(user), SecretSealed: sealed})
	if err != nil {
		return fmt.Errorf("save TOTP: %w", err)
	}
	if rows == 0 {
		return twofactor.ErrAlreadyEnabled
	}
	return nil
}

// Confirm implements twofactor.Store: activation and recovery codes in one transaction.
func (s *TwoFactorStore) Confirm(ctx context.Context, user identity.UserID, step int64, codeHashes [][]byte) (bool, error) {
	confirmed := false
	err := s.db.instanceTx(ctx, func(q *sqlcdb.Queries) error {
		rows, err := q.ConfirmTOTP(ctx, sqlcdb.ConfirmTOTPParams{UserID: uuid.UUID(user), Step: step})
		if err != nil {
			return fmt.Errorf("confirm TOTP: %w", err)
		}
		if rows == 0 {
			return nil
		}
		confirmed = true
		return replaceCodes(ctx, q, user, codeHashes)
	})
	if err != nil {
		return false, err
	}
	return confirmed, nil
}

// UseStep implements twofactor.Store (atomic: of parallel uses only one updates the row).
func (s *TwoFactorStore) UseStep(ctx context.Context, user identity.UserID, step int64) (bool, error) {
	rows, err := s.db.withoutTenant().UseTOTPStep(ctx, sqlcdb.UseTOTPStepParams{UserID: uuid.UUID(user), Step: step})
	if err != nil {
		return false, fmt.Errorf("use TOTP step: %w", err)
	}
	return rows == 1, nil
}

// UseRecoveryCode implements twofactor.Store.
func (s *TwoFactorStore) UseRecoveryCode(ctx context.Context, user identity.UserID, hash []byte) (bool, error) {
	rows, err := s.db.withoutTenant().UseRecoveryCode(ctx, sqlcdb.UseRecoveryCodeParams{UserID: uuid.UUID(user), CodeHash: hash})
	if err != nil {
		return false, fmt.Errorf("use recovery code: %w", err)
	}
	return rows == 1, nil
}

// ReplaceRecoveryCodes implements twofactor.Store.
func (s *TwoFactorStore) ReplaceRecoveryCodes(ctx context.Context, user identity.UserID, codeHashes [][]byte) error {
	return s.db.instanceTx(ctx, func(q *sqlcdb.Queries) error { return replaceCodes(ctx, q, user, codeHashes) })
}

func replaceCodes(ctx context.Context, q *sqlcdb.Queries, user identity.UserID, codeHashes [][]byte) error {
	if err := q.DeleteRecoveryCodes(ctx, uuid.UUID(user)); err != nil {
		return fmt.Errorf("delete recovery codes: %w", err)
	}
	for _, hash := range codeHashes {
		if err := q.InsertRecoveryCode(ctx, sqlcdb.InsertRecoveryCodeParams{UserID: uuid.UUID(user), CodeHash: hash}); err != nil {
			return fmt.Errorf("insert recovery code: %w", err)
		}
	}
	return nil
}

// RecoveryCodesLeft implements twofactor.Store.
func (s *TwoFactorStore) RecoveryCodesLeft(ctx context.Context, user identity.UserID) (int, error) {
	n, err := s.db.withoutTenant().RecoveryCodesLeft(ctx, uuid.UUID(user))
	if err != nil {
		return 0, fmt.Errorf("count recovery codes: %w", err)
	}
	return int(n), nil
}

// Delete implements twofactor.Store.
func (s *TwoFactorStore) Delete(ctx context.Context, user identity.UserID) error {
	return s.db.instanceTx(ctx, func(q *sqlcdb.Queries) error {
		if err := q.DeleteRecoveryCodes(ctx, uuid.UUID(user)); err != nil {
			return fmt.Errorf("delete recovery codes: %w", err)
		}
		return wrap(q.DeleteTOTP(ctx, uuid.UUID(user)), "delete TOTP")
	})
}

// RequiresSecondFactor reports whether the organization of the scope requires two-factor
// authentication.
func (s *TwoFactorStore) RequiresSecondFactor(ctx context.Context, scope tenant.Scope) (bool, error) {
	var required bool
	err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		var err error
		required, err = q.OrganizationRequiresSecondFactor(ctx)
		return wrap(err, "load second-factor requirement")
	})
	if err != nil {
		return false, err
	}
	return required, nil
}

// SetRequiresSecondFactor changes the requirement of the scope's organization with its audit entry.
func (s *TwoFactorStore) SetRequiresSecondFactor(ctx context.Context, scope tenant.Scope, required bool, audit access.AuditEntry) error {
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		rows, err := q.SetOrganizationRequiresSecondFactor(ctx, required)
		if err != nil {
			return fmt.Errorf("set second-factor requirement: %w", err)
		}
		if rows != 1 {
			return access.ErrNotFound
		}
		return appendAudit(ctx, q, audit)
	})
}
