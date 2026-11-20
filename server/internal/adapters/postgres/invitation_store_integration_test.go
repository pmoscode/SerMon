//go:build integration

package postgres_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
	"github.com/pmoscode/SerMon/server/internal/app/invitation"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

func newInvitation(email string, r role.Role, group *tenant.GroupID) invitation.Invitation {
	now := time.Now()
	return invitation.Invitation{ID: invitation.ID(uuid.New()), Email: email, Role: r, Group: group, CreatedAt: now, ExpiresAt: now.Add(invitation.Lifetime)}
}

func uniqueEmail() string { return uuid.NewString() + "@example.org" }

func TestInvitationStore_CreateLookupReplaceRevoke(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	s := postgres.NewInvitationStore(open(t, f.appDSN))
	o, other := newMgmtOrg(t, f), newMgmtOrg(t, f)
	email := uniqueEmail()
	first, second := sha256.Sum256([]byte(uuid.NewString())), sha256.Sum256([]byte(uuid.NewString()))

	inv := newInvitation(email, role.Operator, nil)
	if err := s.Create(ctx, o.scope, inv, first, entry(o.admin, "member.invite")); err != nil {
		t.Fatal(err)
	}
	p, found, err := s.Lookup(ctx, first, time.Now())
	if err != nil || !found || p.Org != o.scope.OrgID() || p.OrgName != "Mgmt" || p.Email != email || p.Role != role.Operator || p.Group != nil {
		t.Fatalf("Lookup() = %+v, %v, %v", p, found, err)
	}
	if _, found, _ := s.Lookup(ctx, first, inv.ExpiresAt.Add(time.Second)); found {
		t.Error("expired invitations must not be found")
	}

	// Inviting the same address again replaces the link.
	replacement := newInvitation(email, role.Viewer, nil)
	if err := s.Create(ctx, o.scope, replacement, second, entry(o.admin, "member.invite")); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.Lookup(ctx, first, time.Now()); found {
		t.Error("the replaced link still works")
	}
	list, err := s.List(ctx, o.scope)
	if err != nil || len(list) != 1 || list[0].ID != replacement.ID || list[0].Role != role.Viewer {
		t.Errorf("List() = %+v, %v", list, err)
	}
	if foreign, _ := s.List(ctx, other.scope); len(foreign) != 0 {
		t.Errorf("another organization sees %d invitations", len(foreign))
	}
	if err := s.Revoke(ctx, other.scope, replacement.ID, entry(other.admin, "invitation.revoke")); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("foreign revoke: %v, want ErrNotFound", err)
	}
	if err := s.Revoke(ctx, o.scope, replacement.ID, entry(o.admin, "invitation.revoke")); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.Lookup(ctx, second, time.Now()); found {
		t.Error("a revoked invitation still works")
	}
	if got := auditActions(t, postgres.NewAccessStore(open(t, f.appDSN)), o.scope); !equalStrings(got, []string{"member.invite", "member.invite", "invitation.revoke"}) {
		t.Errorf("audit = %v", got)
	}
}

func TestInvitationStore_CannotInviteToAForeignGroup(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	s := postgres.NewInvitationStore(open(t, f.appDSN))
	accessStore := postgres.NewAccessStore(open(t, f.appDSN))
	a, b := newMgmtOrg(t, f), newMgmtOrg(t, f)
	groupA := tenant.GroupID(uuid.New())
	if err := accessStore.CreateGroup(ctx, a.scope, access.Group{ID: groupA, Name: "a"}, entry(a.admin, "group.create")); err != nil {
		t.Fatal(err)
	}
	err := s.Create(ctx, b.scope, newInvitation(uniqueEmail(), role.Viewer, &groupA), sha256.Sum256([]byte(uuid.NewString())), entry(b.admin, "member.invite"))
	if !errors.Is(err, access.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func acceptable(t *testing.T, s *postgres.InvitationStore, o mgmtOrg, email string, r role.Role) (invitation.Pending, [32]byte) {
	t.Helper()
	hash := sha256.Sum256([]byte(uuid.NewString()))
	if err := s.Create(context.Background(), o.scope, newInvitation(email, r, nil), hash, entry(o.admin, "member.invite")); err != nil {
		t.Fatal(err)
	}
	p, found, err := s.Lookup(context.Background(), hash, time.Now())
	if err != nil || !found {
		t.Fatalf("Lookup() = %v, %v", found, err)
	}
	return p, hash
}

func TestInvitationStore_AcceptNewAccount(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	db := open(t, f.appDSN)
	s := postgres.NewInvitationStore(db)
	o := newMgmtOrg(t, f)
	email := uniqueEmail()
	p, hash := acceptable(t, s, o, email, role.Operator)
	user := identity.UserID(uuid.New())
	account := invitation.NewAccount{User: user, DisplayName: "Bob", PasswordHash: "$argon2id$v=19$fixture"}

	if err := s.AcceptNewAccount(ctx, p, account, uuid.New(), time.Now(), entry(user, "member.join")); err != nil {
		t.Fatalf("AcceptNewAccount() error = %v", err)
	}
	if _, found, _ := s.Lookup(ctx, hash, time.Now()); found {
		t.Error("the invitation must be consumed")
	}
	if err := s.AcceptNewAccount(ctx, p, invitation.NewAccount{User: identity.UserID(uuid.New()), DisplayName: "Eve", PasswordHash: "$argon2id$x"}, uuid.New(), time.Now(), entry(user, "member.join")); !errors.Is(err, invitation.ErrInvalidInvitation) {
		t.Errorf("second acceptance: %v, want ErrInvalidInvitation", err)
	}
	creds := postgres.NewCredentialStore(db)
	if id, found, _ := creds.FindUserByEmail(ctx, email); !found || id != user {
		t.Errorf("account not created: %v %v", id, found)
	}
	principal, err := access.NewLoader(postgres.NewAccessStore(db), nil).Load(ctx, session.Session{User: user, Org: o.scope.OrgID()})
	if err != nil || len(principal.Bindings) != 1 || principal.Bindings[0].Role != role.Operator || !principal.Bindings[0].AllGroups {
		t.Errorf("principal = %+v, %v", principal, err)
	}
}

func TestInvitationStore_NewAccountForAnExistingAddressFailsAndKeepsTheInvitation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	s := postgres.NewInvitationStore(open(t, f.appDSN))
	o, other := newMgmtOrg(t, f), newMgmtOrg(t, f)
	existing := other.other.String() + "@example.org" // address of a user of another organization
	p, hash := acceptable(t, s, o, existing, role.Viewer)

	err := s.AcceptNewAccount(ctx, p, invitation.NewAccount{User: identity.UserID(uuid.New()), DisplayName: "Eve", PasswordHash: "$argon2id$x"}, uuid.New(), time.Now(), entry(o.admin, "member.join"))
	if !errors.Is(err, invitation.ErrInvalidInvitation) {
		t.Errorf("error = %v, want ErrInvalidInvitation", err)
	}
	if _, found, _ := s.Lookup(ctx, hash, time.Now()); !found {
		t.Error("the invitation must stay usable (the owner of the address can log in and join)")
	}
	if err := s.AcceptExisting(ctx, p, other.other, uuid.New(), entry(other.other, "member.join")); err != nil {
		t.Errorf("AcceptExisting() by the address owner: %v", err)
	}
}

func TestInvitationStore_AcceptingNeverLowersARoleAndHasOneWinner(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	db := open(t, f.appDSN)
	s := postgres.NewInvitationStore(db)
	o := newMgmtOrg(t, f)
	// The org admin is invited again as viewer (on all groups) and accepts twice in parallel.
	p, _ := acceptable(t, s, o, o.admin.String()+"@example.org", role.Viewer)

	var wg sync.WaitGroup
	errs := make([]error, 3)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = s.AcceptExisting(ctx, p, o.admin, uuid.New(), entry(o.admin, "member.join"))
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		} else if !errors.Is(err, invitation.ErrInvalidInvitation) {
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 {
		t.Errorf("%d acceptances succeeded, want 1", ok)
	}
	principal, _ := access.NewLoader(postgres.NewAccessStore(db), nil).Load(ctx, session.Session{User: o.admin, Org: o.scope.OrgID()})
	if len(principal.Bindings) != 1 || principal.Bindings[0].Role != role.Admin {
		t.Errorf("bindings = %+v, want the admin role to stay", principal.Bindings)
	}
}
