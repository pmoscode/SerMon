package access_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	alice = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001"))
	orgA  = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
)

type fakeRepo struct {
	super    bool
	superErr error
	member   bool
	bindings []authz.Binding
	err      error
	calls    int
}

func (f *fakeRepo) MembershipAndBindings(context.Context, tenant.OrgID, identity.UserID) (bool, []authz.Binding, error) {
	f.calls++
	return f.member, f.bindings, f.err
}

func (f *fakeRepo) IsSuperadmin(context.Context, identity.UserID) (bool, error) {
	return f.super, f.superErr
}

func TestLoad_MarksSuperadminsAlsoWithoutOrganization(t *testing.T) {
	p, err := access.NewLoader(&fakeRepo{super: true}).Load(context.Background(), session.Session{User: alice})
	if err != nil || !p.Superadmin {
		t.Errorf("principal = %+v, %v; want superadmin", p, err)
	}
	boom := errors.New("db down")
	if _, err := access.NewLoader(&fakeRepo{superErr: boom}).Load(context.Background(), session.Session{User: alice}); !errors.Is(err, boom) {
		t.Errorf("error = %v, want the repository error", err)
	}
}

func TestLoad_BuildsThePrincipalOfAMember(t *testing.T) {
	repo := &fakeRepo{member: true, bindings: []authz.Binding{{AllGroups: true, Role: role.Admin}}}

	p, err := access.NewLoader(repo).Load(context.Background(), session.Session{User: alice, Org: orgA})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if p.User != alice || p.Org != orgA || len(p.Bindings) != 1 || p.Superadmin {
		t.Errorf("principal = %+v", p)
	}
}

func TestLoad_RemovedMembersLoseAccessImmediately(t *testing.T) {
	repo := &fakeRepo{member: false}

	if _, err := access.NewLoader(repo).Load(context.Background(), session.Session{User: alice, Org: orgA}); !errors.Is(err, access.ErrNoMembership) {
		t.Errorf("Load() error = %v, want ErrNoMembership", err)
	}
}

func TestLoad_SessionWithoutOrganizationHasNoTenantAccess(t *testing.T) {
	repo := &fakeRepo{}

	p, err := access.NewLoader(repo).Load(context.Background(), session.Session{User: alice})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if p.Org != (tenant.OrgID{}) || len(p.Bindings) != 0 || repo.calls != 0 {
		t.Errorf("principal = %+v (repo calls %d), want no organization and no bindings", p, repo.calls)
	}
}

func TestLoad_PropagatesRepositoryErrors(t *testing.T) {
	boom := errors.New("db down")
	if _, err := access.NewLoader(&fakeRepo{err: boom}).Load(context.Background(), session.Session{User: alice, Org: orgA}); !errors.Is(err, boom) {
		t.Errorf("Load() error = %v, want the repository error", err)
	}
}
