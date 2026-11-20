// Package access builds the authorization principal of a request from its session.
package access

import (
	"context"
	"errors"
	"fmt"

	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	// ErrNoMembership means the session's organization no longer has the user as a member.
	ErrNoMembership = errors.New("user is not a member of the session's organization")
	// ErrSecondFactorSetupRequired means the organization requires two-factor authentication and
	// the user has none yet: only the setup is allowed.
	ErrSecondFactorSetupRequired = errors.New("this organization requires two-factor authentication, set it up first")
)

// SecondFactorGate tells whether a member must set up two-factor authentication before acting
// in the organization (twofactor.Service).
type SecondFactorGate interface {
	SetupRequired(ctx context.Context, org tenant.OrgID, user identity.UserID) (bool, error)
}

// Repository reads memberships and role bindings.
type Repository interface {
	MembershipAndBindings(ctx context.Context, org tenant.OrgID, user identity.UserID) (bool, []authz.Binding, error)
	IsSuperadmin(ctx context.Context, user identity.UserID) (bool, error)
}

// Loader builds principals.
type Loader struct {
	repo Repository
	gate SecondFactorGate
}

// NewLoader returns a Loader; gate may be nil (no organization requires a second factor).
func NewLoader(repo Repository, gate SecondFactorGate) *Loader {
	return &Loader{repo: repo, gate: gate}
}

// Load returns the principal of the session. Membership and bindings are read on every request,
// so removing a member or a role takes effect immediately.
func (l *Loader) Load(ctx context.Context, s session.Session) (authz.Principal, error) {
	super, err := l.repo.IsSuperadmin(ctx, s.User)
	if err != nil {
		return authz.Principal{}, fmt.Errorf("load principal: %w", err)
	}
	p := authz.Principal{User: s.User, Superadmin: super}
	if s.Org == (tenant.OrgID{}) {
		return p, nil
	}
	member, bindings, err := l.repo.MembershipAndBindings(ctx, s.Org, s.User)
	if err != nil {
		return authz.Principal{}, fmt.Errorf("load principal: %w", err)
	}
	if !member {
		return authz.Principal{}, ErrNoMembership
	}
	if l.gate != nil {
		required, err := l.gate.SetupRequired(ctx, s.Org, s.User)
		if err != nil {
			return authz.Principal{}, fmt.Errorf("load principal: %w", err)
		}
		if required {
			return authz.Principal{}, ErrSecondFactorSetupRequired
		}
	}
	p.Org, p.Bindings = s.Org, bindings
	return p, nil
}
