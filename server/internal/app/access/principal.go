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

// ErrNoMembership means the session's organization no longer has the user as a member.
var ErrNoMembership = errors.New("user is not a member of the session's organization")

// Repository reads memberships and role bindings.
type Repository interface {
	MembershipAndBindings(ctx context.Context, org tenant.OrgID, user identity.UserID) (bool, []authz.Binding, error)
}

// Loader builds principals.
type Loader struct{ repo Repository }

// NewLoader returns a Loader.
func NewLoader(repo Repository) *Loader { return &Loader{repo: repo} }

// Load returns the principal of the session. Membership and bindings are read on every request,
// so removing a member or a role takes effect immediately.
func (l *Loader) Load(ctx context.Context, s session.Session) (authz.Principal, error) {
	p := authz.Principal{User: s.User}
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
	p.Org, p.Bindings = s.Org, bindings
	return p, nil
}
