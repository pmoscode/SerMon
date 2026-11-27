package organization_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/invitation"
	"github.com/pmoscode/SerMon/server/internal/app/organization"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/quota"
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
	updated struct {
		name   string
		quotas quota.Quotas
	}
	previous organization.Organization
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

func (f *fakeStore) Update(_ context.Context, _ tenant.OrgID, name string, q quota.Quotas, audit func(previous organization.Organization) access.AuditEntry) error {
	f.calls++
	f.updated.name, f.updated.quotas = name, q
	f.audits = append(f.audits, audit(f.previous))
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
	if err := s.Update(ctx, orgAdmin, orgA, "x", quota.Quotas{MaxNodes: 1000}); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("Update by org admin: %v", err)
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
	if a := store.audits[0]; a.Action != "org.create" || a.Actor != super || a.Details["admin_email"] != "" || a.Details["invitation"] == "" {
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
	if err := s.Rename(ctx, superadmin(time.Minute), orgA, "a\nb"); !errors.Is(err, organization.ErrInvalidName) {
		t.Errorf("bad rename: %v", err)
	}
	if store.calls != 0 {
		t.Error("store called with invalid input")
	}
	if err := s.Rename(ctx, superadmin(time.Hour), orgA, "Alpha 2"); !errors.Is(err, access.ErrStepUpRequired) {
		t.Errorf("rename with a stale step-up: %v", err)
	}
	if err := s.Rename(ctx, superadmin(time.Minute), orgA, " Alpha 2 "); err != nil || store.renamed != "Alpha 2" || store.audits[0].Action != "org.rename" {
		t.Errorf("Rename() = %v, renamed %q, audits %+v", err, store.renamed, store.audits)
	}
}

type fakeMailer struct {
	to, text string
	err      error
}

func (f *fakeMailer) SendText(_ context.Context, to, _, text string) error {
	f.to, f.text = to, text
	return f.err
}

// The first admin of a new organization gets the link by mail as well (M1-39): the superadmin
// can't accept it under someone else's address.
func TestCreate_WithAMailerSendsTheFirstAdminLinkByMail(t *testing.T) {
	store := &fakeStore{}
	mailer := &fakeMailer{}
	svc := organization.NewService(store, authz.NewPolicy(), func() time.Time { return now }, uuid.New,
		organization.WithMailer(mailer, "https://sermon.example.com"))

	_, token, err := svc.Create(context.Background(), superadmin(time.Minute), "Beta", "bea@example.org")
	if err != nil || token != "" {
		t.Fatalf("Create() = %q, %v; want no token", token, err)
	}
	i := strings.Index(mailer.text, "/invite#")
	if mailer.to != "bea@example.org" || i < 0 || !strings.Contains(mailer.text, "Beta") {
		t.Fatalf("mail to %q:\n%s", mailer.to, mailer.text)
	}
	link, _, _ := strings.Cut(mailer.text[i+len("/invite#"):], "\n")
	if sha256.Sum256([]byte(link)) != store.hash {
		t.Error("the link in the mail doesn't match the stored hash")
	}

	mailer.err = errors.New("down")
	if _, _, err := svc.Create(context.Background(), superadmin(time.Minute), "Gamma", "gus@example.org"); !errors.Is(err, invitation.ErrMailFailed) {
		t.Errorf("Create() with a failing mailer = %v, want ErrMailFailed", err)
	}
}

// Name and quotas (M2-32): superadmin with a fresh step-up; the audit entry has the old and the
// new state.
func TestUpdate_SetsNameAndQuotasWithAStepUp(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{previous: organization.Organization{Name: "Alpha", Quotas: quota.Quotas{MaxNodes: 10}}}
	s := service(store)
	q := quota.Quotas{MaxNodes: 20, MaxMembers: 5, SamplesPerSecond: 2000, SeriesPerNode: 500, RetentionCapDays: 90}
	if err := s.Update(ctx, superadmin(time.Hour), orgA, "Alpha AG", q); !errors.Is(err, access.ErrStepUpRequired) {
		t.Errorf("stale step-up: %v", err)
	}
	if err := s.Update(ctx, superadmin(time.Minute), orgA, " ", q); !errors.Is(err, organization.ErrInvalidName) {
		t.Errorf("empty name: %v", err)
	}
	if err := s.Update(ctx, superadmin(time.Minute), orgA, "Alpha", quota.Quotas{SeriesPerNode: 100_001}); !errors.Is(err, quota.ErrInvalid) {
		t.Errorf("quota out of range: %v", err)
	}
	if store.calls != 0 {
		t.Fatal("store called")
	}
	if err := s.Update(ctx, superadmin(time.Minute), orgA, " Alpha AG ", q); err != nil {
		t.Fatal(err)
	}
	if store.updated.name != "Alpha AG" || store.updated.quotas != q {
		t.Errorf("updated %+v", store.updated)
	}
	a := store.audits[0]
	want := map[string]string{
		"name": "Alpha AG", "previous_name": "Alpha",
		"quotas":          "max_nodes=20,max_members=5,retention_cap_days=90,samples_per_second=2000,series_per_node=500",
		"previous_quotas": "max_nodes=10",
	}
	if a.Action != "org.update" || a.Actor != super || a.Target != "org:"+orgA.String() {
		t.Errorf("audit %+v", a)
	}
	for k, v := range want {
		if a.Details[k] != v {
			t.Errorf("audit %s = %q, want %q", k, a.Details[k], v)
		}
	}
}
