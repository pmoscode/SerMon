// Package account lets users manage and export their own account and keeps accounts no longer
// than needed (V1-17, KONZEPT §18.7): an account without a membership is deleted after a grace,
// a superadmin deletes one on request, expired invitations go with their address, and the audit
// logs keep their entries for the instance's retention only.
package account

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/naming"
)

// Limits (KONZEPT §18.7).
const (
	// MinDeletionGrace is the shortest time an account without a membership is kept
	// (SERMON_ACCOUNT_DELETION_GRACE, as in the config).
	MinDeletionGrace = 24 * time.Hour
	// CodeLifetime is how long the code of an e-mail change is valid.
	CodeLifetime = time.Hour
	// MaxCodeAttempts is how often a wrong code may be entered before the change is dropped.
	MaxCodeAttempts = 5
	// MaintainInterval is how often accounts, invitations and audit entries are checked.
	MaintainInterval = time.Hour
	// PurgeBatch bounds the accounts one run deletes; the rest follow in the next.
	PurgeBatch = 500
	// ExportFormat names the format of the export.
	ExportFormat = "sermon-account-export/1"

	maxName    = 200
	codeLength = 8
	// Without look-alikes (0/O, 1/I/L, U/V): 29^8 ≈ 2^38 codes, MaxCodeAttempts tries.
	codeAlphabet = "ABCDEFGHJKMNPQRSTWXYZ23456789"
)

var (
	// ErrInvalidName means the display name is empty, too long or contains control characters.
	ErrInvalidName = fmt.Errorf("name must have 1 to %d characters and no control characters", maxName)
	// ErrInvalidEmail means the e-mail address is malformed.
	ErrInvalidEmail = errors.New("invalid e-mail address")
	// ErrUnchanged means the new address is the current one.
	ErrUnchanged = errors.New("this is your address already")
	// ErrEmailTaken means another account has the address.
	ErrEmailTaken = errors.New("another account uses this address")
	// ErrNoPendingChange means there is no change to confirm (none, expired or too many wrong codes).
	ErrNoPendingChange = errors.New("no e-mail change to confirm – request a new code")
	// ErrWrongCode means the code does not match.
	ErrWrongCode = errors.New("the code is wrong")
	// ErrTooManyRequests means the caller asked for too many codes in a short time.
	ErrTooManyRequests = errors.New("too many requests, please wait a moment")
	// ErrMailFailed means the code could not be sent; the change is not stored.
	ErrMailFailed = errors.New("the mail with the code could not be sent")
	// ErrSuperadmin means a superadmin's account can't be deleted.
	ErrSuperadmin = errors.New("a superadmin's account can't be deleted")
	// ErrGraceTooShort means the configured grace is below MinDeletionGrace.
	ErrGraceTooShort = fmt.Errorf("the account deletion grace must be at least %v", MinDeletionGrace)
)

// Profile is the account as its owner sees it.
type Profile struct {
	Email       string
	DisplayName string
	CreatedAt   time.Time
}

// Pending is an e-mail change waiting for its code.
type Pending struct {
	NewEmail  string
	CodeHash  [32]byte
	Attempts  int
	ExpiresAt time.Time
}

// View is the account with its pending change.
type View struct {
	Profile
	Pending *Pending
	// Verified says whether a new address needs the code from a mail (SMTP configured).
	Verified bool
}

// Store persists accounts. Changes write their entry into the instance's audit log in the same
// transaction; Delete also into the log of every organization the account is a member of.
type Store interface {
	Profile(ctx context.Context, user identity.UserID) (Profile, bool, error)
	SetDisplayName(ctx context.Context, user identity.UserID, name string, audit access.AuditEntry) error
	// SetEmail sets the address and drops a pending change; false: another account has it.
	SetEmail(ctx context.Context, user identity.UserID, email string, audit access.AuditEntry) (bool, error)
	PendingChange(ctx context.Context, user identity.UserID) (Pending, bool, error)
	SavePendingChange(ctx context.Context, user identity.UserID, p Pending) error
	// CountAttempt counts a code attempt and returns the attempts so far.
	CountAttempt(ctx context.Context, user identity.UserID) (int, error)
	DeletePendingChange(ctx context.Context, user identity.UserID) error
	// Export reads the account's data and records the export.
	Export(ctx context.Context, user identity.UserID, audit access.AuditEntry) (Export, error)
	FindUser(ctx context.Context, email string) (identity.UserID, bool, error)
	// Delete deletes the account (access.ErrNotFound, ErrSuperadmin, access.ErrLastAdmin) and
	// returns the number of its memberships.
	Delete(ctx context.Context, user identity.UserID, audit access.AuditEntry) (int, error)

	PurgeAccounts(ctx context.Context, before time.Time, limit int) (int, error)
	PurgeInvitations(ctx context.Context, before time.Time) (int, error)
	PurgeEmailChanges(ctx context.Context, before time.Time) (int, error)
	// PruneAudit drops the entries before the time from the audit logs of all organizations and
	// of the instance, each with an entry naming the last dropped one.
	PruneAudit(ctx context.Context, before time.Time) (int, error)
}

// Mailer sends plain text mail (adapters/smtp).
type Mailer interface {
	SendText(ctx context.Context, to, subject, text string) error
}

// Limiter bounds requests per key (platform/ratelimit).
type Limiter interface {
	Allow(key string) bool
}

// Deps are the service's dependencies; Mailer is nil without SMTP (development): then a new
// address applies at once.
type Deps struct {
	Store          Store
	Mailer         Mailer
	Policy         authz.Policy
	Limiter        Limiter
	Now            func() time.Time
	Grace          time.Duration
	AuditRetention time.Duration
	Logger         *slog.Logger
}

// Service is the account of the caller and the maintenance of accounts.
type Service struct{ d Deps }

// NewService returns the service.
func NewService(d Deps) (*Service, error) {
	if d.Grace < MinDeletionGrace {
		return nil, ErrGraceTooShort
	}
	if d.Logger == nil {
		d.Logger = slog.New(slog.DiscardHandler)
	}
	return &Service{d: d}, nil
}

func stepUp(c access.Caller, now time.Time) error {
	if c.Principal.User.IsZero() {
		return access.ErrForbidden
	}
	if now.Sub(c.StepUpAt) > access.StepUpMaxAge {
		return access.ErrStepUpRequired
	}
	return nil
}

func entry(actor identity.UserID, action string, user identity.UserID, details map[string]string) access.AuditEntry {
	return access.AuditEntry{Actor: actor, Action: action, Target: "user:" + user.String(), Details: details}
}

// Get returns the caller's account.
func (s *Service) Get(ctx context.Context, user identity.UserID) (View, error) {
	p, found, err := s.d.Store.Profile(ctx, user)
	if err != nil {
		return View{}, fmt.Errorf("load account: %w", err)
	}
	if !found {
		return View{}, access.ErrNotFound
	}
	v := View{Profile: p, Verified: s.d.Mailer != nil}
	pending, found, err := s.d.Store.PendingChange(ctx, user)
	if err != nil {
		return View{}, fmt.Errorf("load e-mail change: %w", err)
	}
	if found && s.d.Now().Before(pending.ExpiresAt) && pending.Attempts < MaxCodeAttempts {
		v.Pending = &pending
	}
	return v, nil
}

// SetDisplayName changes the caller's name (step-up).
func (s *Service) SetDisplayName(ctx context.Context, c access.Caller, name string) error {
	if err := stepUp(c, s.d.Now()); err != nil {
		return err
	}
	clean, ok := naming.Clean(name, maxName)
	if !ok {
		return ErrInvalidName
	}
	user := c.Principal.User
	if err := s.d.Store.SetDisplayName(ctx, user, clean, entry(user, "account.rename", user, nil)); err != nil {
		return fmt.Errorf("set display name: %w", err)
	}
	return nil
}

// RequestEmailChange starts a change of the caller's address (step-up): with SMTP a code goes to
// the new address and sent is true; without, the address applies at once.
func (s *Service) RequestEmailChange(ctx context.Context, c access.Caller, email string) (sent bool, err error) {
	if err := stepUp(c, s.d.Now()); err != nil {
		return false, err
	}
	email, ok := identity.NormalizeEmail(email)
	if !ok {
		return false, ErrInvalidEmail
	}
	user := c.Principal.User
	current, found, err := s.d.Store.Profile(ctx, user)
	if err != nil {
		return false, fmt.Errorf("load account: %w", err)
	}
	if !found {
		return false, access.ErrNotFound
	}
	if strings.EqualFold(current.Email, email) {
		return false, ErrUnchanged
	}
	if s.d.Limiter != nil && !s.d.Limiter.Allow(user.String()) {
		return false, ErrTooManyRequests
	}
	if s.d.Mailer == nil {
		return false, s.apply(ctx, user, current.Email, email)
	}
	code, err := newCode()
	if err != nil {
		return false, err
	}
	pending := Pending{NewEmail: email, CodeHash: hashCode(user, code), ExpiresAt: s.d.Now().Add(CodeLifetime)}
	if err := s.d.Store.SavePendingChange(ctx, user, pending); err != nil {
		return false, fmt.Errorf("save e-mail change: %w", err)
	}
	if err := s.d.Mailer.SendText(ctx, email, codeSubject, codeText(code)); err != nil {
		if dropErr := s.d.Store.DeletePendingChange(ctx, user); dropErr != nil {
			s.d.Logger.WarnContext(ctx, "account: drop the e-mail change after a failed mail", "error", dropErr)
		}
		return false, fmt.Errorf("%w: %w", ErrMailFailed, err)
	}
	return true, nil
}

// ConfirmEmailChange applies the pending address when code matches; the old address hears of it.
func (s *Service) ConfirmEmailChange(ctx context.Context, user identity.UserID, code string) error {
	pending, found, err := s.d.Store.PendingChange(ctx, user)
	if err != nil {
		return fmt.Errorf("load e-mail change: %w", err)
	}
	if !found || !s.d.Now().Before(pending.ExpiresAt) || pending.Attempts >= MaxCodeAttempts {
		if found {
			if err := s.d.Store.DeletePendingChange(ctx, user); err != nil {
				return fmt.Errorf("drop e-mail change: %w", err)
			}
		}
		return ErrNoPendingChange
	}
	// Counted before the comparison: parallel guesses can't share one attempt.
	attempts, err := s.d.Store.CountAttempt(ctx, user)
	if err != nil {
		return fmt.Errorf("count attempt: %w", err)
	}
	want := pending.CodeHash
	got := hashCode(user, normalizeCode(code))
	if attempts > MaxCodeAttempts || subtle.ConstantTimeCompare(want[:], got[:]) != 1 {
		return ErrWrongCode
	}
	current, found, err := s.d.Store.Profile(ctx, user)
	if err != nil {
		return fmt.Errorf("load account: %w", err)
	}
	if !found {
		return access.ErrNotFound
	}
	return s.apply(ctx, user, current.Email, pending.NewEmail)
}

func (s *Service) apply(ctx context.Context, user identity.UserID, old, email string) error {
	ok, err := s.d.Store.SetEmail(ctx, user, email, entry(user, "account.email_change", user, nil))
	if err != nil {
		return fmt.Errorf("set e-mail: %w", err)
	}
	if !ok {
		return ErrEmailTaken
	}
	if s.d.Mailer != nil {
		// The old mailbox learns of the change, so a taken-over session can't change it unnoticed.
		if err := s.d.Mailer.SendText(ctx, old, changedSubject, changedText); err != nil {
			s.d.Logger.WarnContext(ctx, "account: notice to the old address failed", "error", err)
		}
	}
	return nil
}

// CancelEmailChange drops the caller's pending change.
func (s *Service) CancelEmailChange(ctx context.Context, user identity.UserID) error {
	if err := s.d.Store.DeletePendingChange(ctx, user); err != nil {
		return fmt.Errorf("drop e-mail change: %w", err)
	}
	return nil
}

// Delete deletes the account with the address on request of its owner (superadmin, step-up) and
// returns the number of organizations it left.
func (s *Service) Delete(ctx context.Context, c access.Caller, email string) (int, error) {
	if !s.d.Policy.AllowedInstance(c.Principal, authz.ManageAccounts) {
		return 0, access.ErrForbidden
	}
	if err := stepUp(c, s.d.Now()); err != nil {
		return 0, err
	}
	email, ok := identity.NormalizeEmail(email)
	if !ok {
		return 0, ErrInvalidEmail
	}
	user, found, err := s.d.Store.FindUser(ctx, email)
	if err != nil {
		return 0, fmt.Errorf("find account: %w", err)
	}
	if !found {
		return 0, access.ErrNotFound
	}
	n, err := s.d.Store.Delete(ctx, user, entry(c.Principal.User, "account.delete", user, map[string]string{"reason": "request"}))
	if err != nil {
		return 0, fmt.Errorf("delete account: %w", err)
	}
	return n, nil
}

// Run maintains the accounts every MaintainInterval until ctx ends.
func (s *Service) Run(ctx context.Context) error {
	ticker := time.NewTicker(MaintainInterval)
	defer ticker.Stop()
	for {
		if err := s.Maintain(ctx); err != nil {
			s.d.Logger.WarnContext(ctx, "account maintenance", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Maintain deletes accounts without membership after the grace, expired invitations and e-mail
// changes, and audit entries older than the retention; a failing step doesn't stop the others.
func (s *Service) Maintain(ctx context.Context) error {
	now := s.d.Now()
	var errs []error
	accounts, err := s.d.Store.PurgeAccounts(ctx, now.Add(-s.d.Grace), PurgeBatch)
	if err != nil {
		errs = append(errs, fmt.Errorf("delete accounts: %w", err))
	}
	invitations, err := s.d.Store.PurgeInvitations(ctx, now)
	if err != nil {
		errs = append(errs, fmt.Errorf("delete expired invitations: %w", err))
	}
	if _, err := s.d.Store.PurgeEmailChanges(ctx, now); err != nil {
		errs = append(errs, fmt.Errorf("delete expired e-mail changes: %w", err))
	}
	var audit int
	if s.d.AuditRetention > 0 {
		if audit, err = s.d.Store.PruneAudit(ctx, now.Add(-s.d.AuditRetention)); err != nil {
			errs = append(errs, fmt.Errorf("prune audit logs: %w", err))
		}
	}
	if accounts+invitations+audit > 0 {
		s.d.Logger.InfoContext(ctx, "deleted personal data past its period",
			"accounts", accounts, "invitations", invitations, "audit_entries", audit)
	}
	return errors.Join(errs...)
}

func newCode() (string, error) {
	raw := make([]byte, codeLength)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate code: %w", err)
	}
	var b strings.Builder
	for _, r := range raw {
		// 256 % 29 leaves a small bias; with MaxCodeAttempts tries it doesn't matter.
		b.WriteByte(codeAlphabet[int(r)%len(codeAlphabet)])
	}
	return b.String(), nil
}

// normalizeCode accepts the code as typed: any case, spaces and dashes.
func normalizeCode(code string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' {
			return -1
		}
		return r
	}, strings.ToUpper(strings.TrimSpace(code)))
}

// hashCode binds the code to the account: a code is worth nothing for another one.
func hashCode(user identity.UserID, code string) [32]byte {
	return sha256.Sum256([]byte(user.String() + "\x1f" + code))
}

const (
	codeSubject    = "SerMon: Neue E-Mail-Adresse bestätigen / Confirm your new e-mail address"
	changedSubject = "SerMon: E-Mail-Adresse geändert / E-mail address changed"
	changedText    = "Die E-Mail-Adresse deines SerMon-Kontos wurde geändert. Warst du das nicht, " +
		"melde dich bei einem Admin deiner Organisation.\n\n" +
		"The e-mail address of your SerMon account was changed. If this wasn't you, contact an " +
		"admin of your organization.\n"
)

func codeText(code string) string {
	return "Dein Code zum Bestätigen der neuen E-Mail-Adresse: " + code + "\n" +
		"Gib ihn in SerMon unter „Konto“ ein. Er gilt eine Stunde.\n\n" +
		"Your code to confirm the new e-mail address: " + code + "\n" +
		"Enter it in SerMon under \"Account\". It is valid for one hour.\n\n" +
		"Hast du das nicht angefordert, ignoriere diese Nachricht.\n" +
		"If you didn't ask for this, ignore this message.\n"
}
