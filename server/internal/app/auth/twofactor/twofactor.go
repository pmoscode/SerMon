// Package twofactor manages the second factor of a user (KONZEPT §7, M1-03): TOTP with
// recovery codes. The TOTP secret is stored sealed with the secrets box (bound to the user), the
// recovery codes only as hashes. Every TOTP step and every recovery code works once.
package twofactor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/auth/recovery"
	"github.com/pmoscode/SerMon/server/internal/app/auth/totp"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	"github.com/pmoscode/SerMon/server/internal/secrets"
)

// Issuer is the name authenticator apps show for the entry.
const Issuer = "SerMon"

var (
	// ErrAlreadyEnabled means the user has a confirmed second factor (disable it first).
	ErrAlreadyEnabled = errors.New("two-factor authentication is already enabled")
	// ErrNotEnabled means the user has no confirmed second factor.
	ErrNotEnabled = errors.New("two-factor authentication is not enabled")
	// ErrNoEnrollment means there is no started setup to confirm.
	ErrNoEnrollment = errors.New("no two-factor setup to confirm, start again")
	// ErrInvalidCode means the code is wrong, expired or already used.
	ErrInvalidCode = errors.New("the code is wrong or was already used")
	// ErrOwnSecondFactorFirst means an admin must set up a second factor before requiring one.
	ErrOwnSecondFactorFirst = errors.New("set up your own two-factor authentication before requiring it")
)

// Secret is the stored TOTP state of a user.
type Secret struct {
	Sealed    []byte
	Confirmed bool
	LastStep  int64
}

// Store persists the second factors (instance-level, per user).
type Store interface {
	Get(ctx context.Context, user identity.UserID) (Secret, bool, error)
	// SavePending stores a new, unconfirmed secret; ErrAlreadyEnabled if a confirmed one exists.
	SavePending(ctx context.Context, user identity.UserID, sealed []byte) error
	// Confirm activates the pending secret with the used step and stores the recovery codes;
	// false if there is no pending secret.
	Confirm(ctx context.Context, user identity.UserID, step int64, codeHashes [][]byte) (bool, error)
	// UseStep records a used TOTP step; false if it (or a later one) was used already.
	UseStep(ctx context.Context, user identity.UserID, step int64) (bool, error)
	// UseRecoveryCode marks an unused code as used; false if there is none with the hash.
	UseRecoveryCode(ctx context.Context, user identity.UserID, hash []byte) (bool, error)
	// ReplaceRecoveryCodes stores a new set (the old one stops working).
	ReplaceRecoveryCodes(ctx context.Context, user identity.UserID, codeHashes [][]byte) error
	// RecoveryCodesLeft counts the unused codes.
	RecoveryCodesLeft(ctx context.Context, user identity.UserID) (int, error)
	// Delete removes secret and codes.
	Delete(ctx context.Context, user identity.UserID) error
	// RequiresSecondFactor reports the requirement of the scope's organization.
	RequiresSecondFactor(ctx context.Context, scope tenant.Scope) (bool, error)
	// SetRequiresSecondFactor changes it and writes the audit entry in the same transaction.
	SetRequiresSecondFactor(ctx context.Context, scope tenant.Scope, required bool, audit access.AuditEntry) error
}

// Status describes the second factor of a user.
type Status struct {
	Enabled           bool
	RecoveryCodesLeft int
}

// Enrollment is a started TOTP setup: the secret for the app.
type Enrollment struct {
	Secret string // base32 for manual entry
	URI    string // otpauth:// for the QR code
}

// Service manages and checks second factors.
type Service struct {
	store  Store
	box    *secrets.Box
	policy authz.Policy
	now    func() time.Time
}

// NewService returns the service.
func NewService(store Store, box *secrets.Box, policy authz.Policy, now func() time.Time) *Service {
	return &Service{store: store, box: box, policy: policy, now: now}
}

// OrganizationRequirement reports whether the caller's organization requires a second factor
// (any member may read it).
func (s *Service) OrganizationRequirement(ctx context.Context, c access.Caller) (bool, error) {
	scope, err := tenant.ForAllGroups(c.Principal.Org)
	if err != nil {
		return false, access.ErrForbidden
	}
	required, err := s.store.RequiresSecondFactor(ctx, scope)
	if err != nil {
		return false, fmt.Errorf("second-factor requirement: %w", err)
	}
	return required, nil
}

// SetOrganizationRequirement makes a second factor mandatory (or optional) in the caller's
// organization: admins for all groups, with step-up. Requiring it needs the caller's own second
// factor, so that nobody locks themselves out of the settings.
func (s *Service) SetOrganizationRequirement(ctx context.Context, c access.Caller, required bool) error {
	scope, err := access.Authorize(s.policy, c, authz.ManagePolicies, true, s.now())
	if err != nil {
		return err //nolint:wrapcheck // access errors are mapped by the transport
	}
	if required {
		enabled, err := s.Enabled(ctx, c.Principal.User)
		if err != nil {
			return err
		}
		if !enabled {
			return ErrOwnSecondFactorFirst
		}
	}
	audit := access.AuditEntry{
		Actor: c.Principal.User, Action: "org.second_factor_requirement", Target: "org:" + c.Principal.Org.String(),
		Details: map[string]string{"required": fmt.Sprint(required)},
	}
	if err := s.store.SetRequiresSecondFactor(ctx, scope, required, audit); err != nil {
		return fmt.Errorf("set second-factor requirement: %w", err)
	}
	return nil
}

func associatedData(user identity.UserID) []byte { return []byte("totp:" + user.String()) }

// Enabled reports whether the user has a confirmed second factor.
func (s *Service) Enabled(ctx context.Context, user identity.UserID) (bool, error) {
	secret, found, err := s.store.Get(ctx, user)
	if err != nil {
		return false, fmt.Errorf("two-factor status: %w", err)
	}
	return found && secret.Confirmed, nil
}

// SetupRequired reports whether the organization requires a second factor the user doesn't have
// (access.SecondFactorGate).
func (s *Service) SetupRequired(ctx context.Context, org tenant.OrgID, user identity.UserID) (bool, error) {
	scope, err := tenant.ForAllGroups(org)
	if err != nil {
		return false, fmt.Errorf("second-factor requirement: %w", err)
	}
	required, err := s.store.RequiresSecondFactor(ctx, scope)
	if err != nil || !required {
		return false, err //nolint:wrapcheck // the store describes it
	}
	enabled, err := s.Enabled(ctx, user)
	return !enabled, err
}

// Status returns the second factor of the user.
func (s *Service) Status(ctx context.Context, user identity.UserID) (Status, error) {
	enabled, err := s.Enabled(ctx, user)
	if err != nil || !enabled {
		return Status{}, err
	}
	left, err := s.store.RecoveryCodesLeft(ctx, user)
	if err != nil {
		return Status{}, fmt.Errorf("two-factor status: %w", err)
	}
	return Status{Enabled: true, RecoveryCodesLeft: left}, nil
}

func (s *Service) requireStepUp(stepUpAt time.Time) error {
	if s.now().Sub(stepUpAt) > access.StepUpMaxAge {
		return access.ErrStepUpRequired
	}
	return nil
}

// Begin starts the TOTP setup with a new secret. It needs a recent step-up, so that a taken-over
// session can't bind the attacker's app.
func (s *Service) Begin(ctx context.Context, user identity.UserID, account string, stepUpAt time.Time) (Enrollment, error) {
	if err := s.requireStepUp(stepUpAt); err != nil {
		return Enrollment{}, err
	}
	secret, err := totp.GenerateSecret()
	if err != nil {
		return Enrollment{}, err //nolint:wrapcheck // already describes the failure
	}
	sealed, err := s.box.Seal(secret, associatedData(user))
	if err != nil {
		return Enrollment{}, fmt.Errorf("seal TOTP secret: %w", err)
	}
	if err := s.store.SavePending(ctx, user, sealed); err != nil {
		return Enrollment{}, fmt.Errorf("start two-factor setup: %w", err)
	}
	return Enrollment{Secret: totp.EncodeSecret(secret), URI: totp.KeyURI(Issuer, account, secret)}, nil
}

// Confirm finishes the setup with a code from the app and returns the recovery codes (shown once).
func (s *Service) Confirm(ctx context.Context, user identity.UserID, code string) ([]string, error) {
	stored, found, err := s.store.Get(ctx, user)
	switch {
	case err != nil:
		return nil, fmt.Errorf("confirm two-factor setup: %w", err)
	case found && stored.Confirmed:
		return nil, ErrAlreadyEnabled
	case !found:
		return nil, ErrNoEnrollment
	}
	secret, err := s.box.Open(stored.Sealed, associatedData(user))
	if err != nil {
		return nil, fmt.Errorf("open TOTP secret: %w", err)
	}
	step, ok := totp.Verify(secret, code, s.now(), stored.LastStep)
	if !ok {
		return nil, ErrInvalidCode
	}
	codes, hashes, err := newRecoveryCodes()
	if err != nil {
		return nil, err
	}
	confirmed, err := s.store.Confirm(ctx, user, step, hashes)
	if err != nil {
		return nil, fmt.Errorf("confirm two-factor setup: %w", err)
	}
	if !confirmed {
		return nil, ErrNoEnrollment
	}
	return codes, nil
}

// Verify checks a TOTP code or a recovery code of a user with a confirmed second factor. Each
// works once. A wrong code is (false, nil); the caller counts it towards the lockout.
func (s *Service) Verify(ctx context.Context, user identity.UserID, code string) (bool, error) {
	stored, found, err := s.store.Get(ctx, user)
	if err != nil {
		return false, fmt.Errorf("verify second factor: %w", err)
	}
	if !found || !stored.Confirmed {
		return false, nil
	}
	if recovery.LooksLikeCode(code) {
		used, err := s.store.UseRecoveryCode(ctx, user, recovery.Hash(code))
		if err != nil {
			return false, fmt.Errorf("use recovery code: %w", err)
		}
		return used, nil
	}
	secret, err := s.box.Open(stored.Sealed, associatedData(user))
	if err != nil {
		return false, fmt.Errorf("open TOTP secret: %w", err)
	}
	step, ok := totp.Verify(secret, code, s.now(), stored.LastStep)
	if !ok {
		return false, nil
	}
	// Atomic in the store: of two parallel requests with the same code only one wins.
	used, err := s.store.UseStep(ctx, user, step)
	if err != nil {
		return false, fmt.Errorf("use TOTP step: %w", err)
	}
	return used, nil
}

// Disable removes the second factor; it needs a recent step-up (with the second factor).
func (s *Service) Disable(ctx context.Context, user identity.UserID, stepUpAt time.Time) error {
	if err := s.requireStepUp(stepUpAt); err != nil {
		return err
	}
	if err := s.store.Delete(ctx, user); err != nil {
		return fmt.Errorf("disable two-factor authentication: %w", err)
	}
	return nil
}

// RegenerateRecoveryCodes replaces the recovery codes; it needs a recent step-up.
func (s *Service) RegenerateRecoveryCodes(ctx context.Context, user identity.UserID, stepUpAt time.Time) ([]string, error) {
	if err := s.requireStepUp(stepUpAt); err != nil {
		return nil, err
	}
	enabled, err := s.Enabled(ctx, user)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, ErrNotEnabled
	}
	codes, hashes, err := newRecoveryCodes()
	if err != nil {
		return nil, err
	}
	if err := s.store.ReplaceRecoveryCodes(ctx, user, hashes); err != nil {
		return nil, fmt.Errorf("replace recovery codes: %w", err)
	}
	return codes, nil
}

func newRecoveryCodes() ([]string, [][]byte, error) {
	codes, err := recovery.Generate()
	if err != nil {
		return nil, nil, err //nolint:wrapcheck // already describes the failure
	}
	hashes := make([][]byte, len(codes))
	for i, c := range codes {
		hashes[i] = recovery.Hash(c)
	}
	return codes, hashes, nil
}
