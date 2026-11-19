package organization_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/invitation"
	"github.com/pmoscode/SerMon/server/internal/app/organization"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	now   = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	super = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001"))
	orgA  = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
)

type fakeStore struct {
	calls   int
	created organization.Organization
	invite  invitation.Invitation
	hash    [32]byte
	audits  []access.AuditEntry
	renamed string
}

func (f *fakeStore) List(context.Context) ([]organization.Organization, error) {
	f.calls++
	return []organization.Organization{{ID: orgA, Name: "Alpha"}}, nil
}

func (f *fakeStore) Create(_ context.Context, o organization.Organization, inv invitation.Invitation, hash [32]byte, a access.AuditEntry) error {
	f.calls++
	f.created, f.invite, f.hash = o, inv, hash
	f.audits = append(f.audits, a)
	return nil
}

func (f *fakeStore) Rename(_ context.Context, _ tenant.OrgID, name string, a access.AuditEntry) error {
	f.calls++
	f.renamed = name
	f.audits = append(f.audits, a)
	return nil
}

func service(store *fakeStore) *organization.Service {
	return organization.NewService(store, authz.NewPolicy(), func() time.Time { return now }, uuid.New)
}

func superadmin(stepUp time.Duration) access.Caller {
	return access.Caller{Principal: authz.Principal{User: super, Superadmin: true}, StepUpAt: now.Add(-stepUp)}
}

func TestOnlySuperadminsManageOrganizations(t *testing.T) {
	ctx := context.Background()
	orgAdmin := access.Caller{
		Principal: authz.Principal{User: super, Org: orgA, Bindings: []authz.Binding{{AllGroups: true, Role: role.Admin}}},
		StepUpAt:  now,
	}
	store := &fakeStore{}
	s := service(store)
	if _, err := s.List(ctx, orgAdmin); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("List by org admin: %v", err)
	}
	if _, _, err := s.Create(ctx, orgAdmin, "Beta", "bea@example.org"); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("Create by org admin: %v", err)
	}
	if err := s.Rename(ctx, orgAdmin, orgA, "x"); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("Rename by org admin: %v", err)
	}
	if store.calls != 0 {
		t.Error("store called although access was denied")
	}
	if orgs, err := s.List(ctx, superadmin(time.Hour)); err != nil || len(orgs) != 1 {
		t.Errorf("List by superadmin = %v, %v", orgs, err)
	}
}

func TestCreate_InvitesTheFirstAdminAndNeedsAStepUp(t *testing.T) {
	store := &fakeStore{}
	org, token, err := service(store).Create(context.Background(), superadmin(time.Minute), " Beta ", "Bea@Example.org")
	if err != nil {
		t.Fatal(err)
	}
	if org.Name != "Beta" || store.created.ID != org.ID {
		t.Errorf("organization = %+v", org)
	}
	inv := store.invite
	if inv.Email != "bea@example.org" || inv.Role != role.Admin || inv.Group != nil || inv.InvitedBy == nil || *inv.InvitedBy != super ||
		!inv.ExpiresAt.Equal(now.Add(invitation.Lifetime)) {
		t.Errorf("first admin invitation = %+v", inv)
	}
	if store.hash != sha256.Sum256([]byte(token)) || len(token) != 43 {
		t.Error("token/hash mismatch")
	}
	if a := store.audits[0]; a.Action != "org.create" || a.Actor != super || a.Details["admin_email"] != "bea@example.org" {
		t.Errorf("audit = %+v", a)
	}

	if _, _, err := service(&fakeStore{}).Create(context.Background(), superadmin(time.Hour), "Beta", "bea@example.org"); !errors.Is(err, access.ErrStepUpRequired) {
		t.Errorf("stale step-up: %v", err)
	}
}

func TestCreateAndRename_ValidateInput(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{}
	s := service(store)
	if _, _, err := s.Create(ctx, superadmin(time.Minute), " ", "bea@example.org"); !errors.Is(err, organization.ErrInvalidName) {
		t.Errorf("empty name: %v", err)
	}
	if _, _, err := s.Create(ctx, superadmin(time.Minute), "Beta", "no-mail"); !errors.Is(err, invitation.ErrInvalidEmail) {
		t.Errorf("bad email: %v", err)
	}
	if err := s.Rename(ctx, superadmin(time.Hour), orgA, "a\nb"); !errors.Is(err, organization.ErrInvalidName) {
		t.Errorf("bad rename: %v", err)
	}
	if store.calls != 0 {
		t.Error("store called with invalid input")
	}
	if err := s.Rename(ctx, superadmin(time.Hour), orgA, " Alpha 2 "); err != nil || store.renamed != "Alpha 2" || store.audits[0].Action != "org.rename" {
		t.Errorf("Rename() = %v, renamed %q, audits %+v", err, store.renamed, store.audits)
	}
}
