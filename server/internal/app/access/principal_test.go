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
	noLogs   bool
	err      error
	calls    int
}

func (f *fakeRepo) MembershipAndBindings(context.Context, tenant.OrgID, identity.UserID) (access.Membership, error) {
	f.calls++
	return access.Membership{Member: f.member, Bindings: f.bindings, ViewersWithoutLogs: f.noLogs}, f.err
}

func (f *fakeRepo) IsSuperadmin(context.Context, identity.UserID) (bool, error) {
	return f.super, f.superErr
}

func TestLoad_MarksSuperadminsAlsoWithoutOrganization(t *testing.T) {
	p, err := access.NewLoader(&fakeRepo{super: true}, nil).Load(context.Background(), session.Session{User: alice})
	if err != nil || !p.Superadmin {
		t.Errorf("principal = %+v, %v; want superadmin", p, err)
	}
	boom := errors.New("db down")
	if _, err := access.NewLoader(&fakeRepo{superErr: boom}, nil).Load(context.Background(), session.Session{User: alice}); !errors.Is(err, boom) {
		t.Errorf("error = %v, want the repository error", err)
	}
}

func TestLoad_TakesTheViewerSwitchOfTheOrganization(t *testing.T) {
	repo := &fakeRepo{member: true, bindings: []authz.Binding{{AllGroups: true, Role: role.Viewer}}, noLogs: true}
	p, err := access.NewLoader(repo, nil).Load(context.Background(), session.Session{User: alice, Org: orgA})
	if err != nil || !p.ViewersWithoutLogs {
		t.Errorf("principal = %+v, %v", p, err)
	}
}

func TestLoad_BuildsThePrincipalOfAMember(t *testing.T) {
	repo := &fakeRepo{member: true, bindings: []authz.Binding{{AllGroups: true, Role: role.Admin}}}

	p, err := access.NewLoader(repo, nil).Load(context.Background(), session.Session{User: alice, Org: orgA})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if p.User != alice || p.Org != orgA || len(p.Bindings) != 1 || p.Superadmin {
		t.Errorf("principal = %+v", p)
	}
}

func TestLoad_RemovedMembersLoseAccessImmediately(t *testing.T) {
	repo := &fakeRepo{member: false}

	if _, err := access.NewLoader(repo, nil).Load(context.Background(), session.Session{User: alice, Org: orgA}); !errors.Is(err, access.ErrNoMembership) {
		t.Errorf("Load() error = %v, want ErrNoMembership", err)
	}
}

func TestLoad_SessionWithoutOrganizationHasNoTenantAccess(t *testing.T) {
	repo := &fakeRepo{}

	p, err := access.NewLoader(repo, nil).Load(context.Background(), session.Session{User: alice})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if p.Org != (tenant.OrgID{}) || len(p.Bindings) != 0 || repo.calls != 0 {
		t.Errorf("principal = %+v (repo calls %d), want no organization and no bindings", p, repo.calls)
	}
}

func TestLoad_PropagatesRepositoryErrors(t *testing.T) {
	boom := errors.New("db down")
	if _, err := access.NewLoader(&fakeRepo{err: boom}, nil).Load(context.Background(), session.Session{User: alice, Org: orgA}); !errors.Is(err, boom) {
		t.Errorf("Load() error = %v, want the repository error", err)
	}
}

type gate struct{ required bool }

func (g gate) SetupRequired(context.Context, tenant.OrgID, identity.UserID) (bool, error) {
	return g.required, nil
}

func TestLoad_OrganizationsCanRequireASecondFactor(t *testing.T) {
	repo := &fakeRepo{member: true}
	s := session.Session{User: alice, Org: orgA}

	if _, err := access.NewLoader(repo, gate{required: true}).Load(context.Background(), s); !errors.Is(err, access.ErrSecondFactorSetupRequired) {
		t.Errorf("Load() = %v, want ErrSecondFactorSetupRequired", err)
	}
	if _, err := access.NewLoader(repo, gate{}).Load(context.Background(), s); err != nil {
		t.Errorf("Load() without requirement = %v", err)
	}
	if _, err := access.NewLoader(repo, gate{required: true}).Load(context.Background(), session.Session{User: alice}); err != nil {
		t.Errorf("instance sessions have no organization requirement: %v", err)
	}
	// M3-20: a login with several factors at the OIDC provider satisfies it.
	if _, err := access.NewLoader(repo, gate{required: true}).Load(context.Background(), session.Session{User: alice, Org: orgA, ExternalMFA: true}); err != nil {
		t.Errorf("a session with the provider's second factor: %v", err)
	}
}
