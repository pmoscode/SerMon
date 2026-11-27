package account

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/access"
)

// Export is what SerMon holds about one account (Art. 15/20 GDPR): only the account's own data –
// no secrets (hashes, keys, seeds), nothing of other people, audit entries without their details
// (those name others, e.g. an invited address).
type Export struct {
	Account     ExportedAccount     `json:"account"`
	Password    *ExportedPassword   `json:"password,omitempty"`
	TwoFactor   ExportedTwoFactor   `json:"two_factor"`
	Passkeys    []ExportedPasskey   `json:"passkeys"`
	Identities  []ExportedIdentity  `json:"sso_identities"`
	Sessions    []ExportedSession   `json:"sessions"`
	Memberships []ExportedMember    `json:"memberships"`
	Dashboards  []ExportedDashboard `json:"dashboards"`
	Audit       []ExportedAudit     `json:"audit_entries"`
	EmailChange *ExportedChange     `json:"pending_email_change,omitempty"`
}

// ExportedAccount is the account itself.
type ExportedAccount struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
	CreatedAt   time.Time `json:"created_at"`
}

// ExportedPassword says that the account has a password and since when.
type ExportedPassword struct {
	ChangedAt time.Time `json:"changed_at"`
}

// ExportedTwoFactor describes the second factor without its secret.
type ExportedTwoFactor struct {
	TOTP              bool       `json:"totp"`
	TOTPSince         *time.Time `json:"totp_since,omitempty"`
	RecoveryCodesLeft int        `json:"recovery_codes_left"`
}

// ExportedPasskey is a passkey without its key.
type ExportedPasskey struct {
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// ExportedIdentity is a linked identity at the provider.
type ExportedIdentity struct {
	Issuer      string     `json:"issuer"`
	Subject     string     `json:"subject"`
	Email       string     `json:"email,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
}

// ExportedSession is a session without its token.
type ExportedSession struct {
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
}

// ExportedMember is a membership with its roles.
type ExportedMember struct {
	Organization string         `json:"organization"`
	JoinedAt     time.Time      `json:"joined_at"`
	Roles        []ExportedRole `json:"roles"`
}

// ExportedRole is a role on a group ("*": all groups).
type ExportedRole struct {
	Group  string `json:"group"`
	Role   string `json:"role"`
	Origin string `json:"origin"`
}

// ExportedDashboard is a dashboard the account owns.
type ExportedDashboard struct {
	Organization string    `json:"organization"`
	Title        string    `json:"title"`
	Shared       bool      `json:"shared"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// ExportedAudit is an action of the account in an organization's audit log.
type ExportedAudit struct {
	Organization string    `json:"organization"`
	At           time.Time `json:"at"`
	Action       string    `json:"action"`
}

// ExportedChange is a pending change of the address.
type ExportedChange struct {
	NewEmail  string    `json:"new_email"`
	ExpiresAt time.Time `json:"expires_at"`
}

type exportFile struct {
	Format     string    `json:"format"`
	ExportedAt time.Time `json:"exported_at"`
	Export
}

// Export returns the caller's data as JSON (step-up: it is all of it at once); the export is
// recorded in the instance's audit log.
func (s *Service) Export(ctx context.Context, c access.Caller) ([]byte, error) {
	now := s.d.Now()
	if err := stepUp(c, now); err != nil {
		return nil, err
	}
	user := c.Principal.User
	data, err := s.d.Store.Export(ctx, user, entry(user, "account.export", user, nil))
	if err != nil {
		return nil, fmt.Errorf("export account: %w", err)
	}
	out, err := json.MarshalIndent(exportFile{Format: ExportFormat, ExportedAt: now.UTC(), Export: data}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode export: %w", err)
	}
	return out, nil
}
