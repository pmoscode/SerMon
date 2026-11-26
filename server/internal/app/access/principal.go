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

// Membership is what a user is in an organization.
type Membership struct {
	Member   bool
	Bindings []authz.Binding
	// ViewersWithoutLogs is the organization's switch (M3-12).
	ViewersWithoutLogs bool
}

// Repository reads memberships and role bindings.
type Repository interface {
	MembershipAndBindings(ctx context.Context, org tenant.OrgID, user identity.UserID) (Membership, error)
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
	membership, err := l.repo.MembershipAndBindings(ctx, s.Org, s.User)
	if err != nil {
		return authz.Principal{}, fmt.Errorf("load principal: %w", err)
	}
	if !membership.Member {
		return authz.Principal{}, ErrNoMembership
	}
	// A login with several factors at the OIDC provider satisfies the requirement (M3-20).
	if l.gate != nil && !s.ExternalMFA {
		required, err := l.gate.SetupRequired(ctx, s.Org, s.User)
		if err != nil {
			return authz.Principal{}, fmt.Errorf("load principal: %w", err)
		}
		if required {
			return authz.Principal{}, ErrSecondFactorSetupRequired
		}
	}
	p.Org, p.Bindings, p.ViewersWithoutLogs = s.Org, membership.Bindings, membership.ViewersWithoutLogs
	return p, nil
}

// ForUser returns the principal of a member as it is now, without instance rights and without a
// session (a kiosk display, M5-26). ErrNoMembership if the user left the organization.
func (l *Loader) ForUser(ctx context.Context, org tenant.OrgID, user identity.UserID) (authz.Principal, error) {
	membership, err := l.repo.MembershipAndBindings(ctx, org, user)
	if err != nil {
		return authz.Principal{}, fmt.Errorf("load principal: %w", err)
	}
	if !membership.Member {
		return authz.Principal{}, ErrNoMembership
	}
	return authz.Principal{User: user, Org: org, Bindings: membership.Bindings, ViewersWithoutLogs: membership.ViewersWithoutLogs}, nil
}
