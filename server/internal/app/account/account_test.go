package account_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/account"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	now   = time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	alice = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000a11c"))
	bob   = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000b0b"))
	sam   = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000555"))
)

// fakeStore keeps accounts by e-mail like the database: an address belongs to one account.
type fakeStore struct {
	profiles  map[identity.UserID]account.Profile
	pending   map[identity.UserID]account.Pending
	audit     []access.AuditEntry
	deleted   []identity.UserID
	deleteErr error
	cutoffs   map[string]time.Time
	failStep  string
}

func newStore() *fakeStore {
	return &fakeStore{
		profiles: map[identity.UserID]account.Profile{
			alice: {Email: "alice@example.org", DisplayName: "Alice", CreatedAt: now.Add(-time.Hour)},
			bob:   {Email: "bob@example.org", DisplayName: "Bob"},
		},
		pending: map[identity.UserID]account.Pending{},
		cutoffs: map[string]time.Time{},
	}
}

func (f *fakeStore) Profile(_ context.Context, user identity.UserID) (account.Profile, bool, error) {
	p, ok := f.profiles[user]
	return p, ok, nil
}

func (f *fakeStore) SetDisplayName(_ context.Context, user identity.UserID, name string, audit access.AuditEntry) error {
	p := f.profiles[user]
	p.DisplayName = name
	f.profiles[user] = p
	f.audit = append(f.audit, audit)
	return nil
}

func (f *fakeStore) SetEmail(_ context.Context, user identity.UserID, email string, audit access.AuditEntry) (bool, error) {
	for other, p := range f.profiles {
		if other != user && p.Email == email {
			return false, nil
		}
	}
	p := f.profiles[user]
	p.Email = email
	f.profiles[user] = p
	delete(f.pending, user)
	f.audit = append(f.audit, audit)
	return true, nil
}

func (f *fakeStore) PendingChange(_ context.Context, user identity.UserID) (account.Pending, bool, error) {
	p, ok := f.pending[user]
	return p, ok, nil
}

func (f *fakeStore) SavePendingChange(_ context.Context, user identity.UserID, p account.Pending) error {
	f.pending[user] = p
	return nil
}

func (f *fakeStore) CountAttempt(_ context.Context, user identity.UserID) (int, error) {
	p := f.pending[user]
	p.Attempts++
	f.pending[user] = p
	return p.Attempts, nil
}

func (f *fakeStore) DeletePendingChange(_ context.Context, user identity.UserID) error {
	delete(f.pending, user)
	return nil
}

func (f *fakeStore) Export(_ context.Context, user identity.UserID, audit access.AuditEntry) (account.Export, error) {
	f.audit = append(f.audit, audit)
	p := f.profiles[user]
	return account.Export{Account: account.ExportedAccount{ID: user.String(), Email: p.Email, DisplayName: p.DisplayName, CreatedAt: p.CreatedAt}}, nil
}

func (f *fakeStore) FindUser(_ context.Context, email string) (identity.UserID, bool, error) {
	for id, p := range f.profiles {
		if p.Email == email {
			return id, true, nil
		}
	}
	return identity.UserID{}, false, nil
}

func (f *fakeStore) Delete(_ context.Context, user identity.UserID, audit access.AuditEntry) (int, error) {
	if f.deleteErr != nil {
		return 0, f.deleteErr
	}
	f.deleted = append(f.deleted, user)
	f.audit = append(f.audit, audit)
	return 2, nil
}

func (f *fakeStore) step(name string, before time.Time) error {
	f.cutoffs[name] = before
	if f.failStep == name {
		return errors.New("database down")
	}
	return nil
}

func (f *fakeStore) PurgeAccounts(_ context.Context, before time.Time, limit int) (int, error) {
	if limit != account.PurgeBatch {
		return 0, errors.New("unbounded purge")
	}
	return 1, f.step("accounts", before)
}

func (f *fakeStore) PurgeInvitations(_ context.Context, before time.Time) (int, error) {
	return 1, f.step("invitations", before)
}

func (f *fakeStore) PurgeEmailChanges(_ context.Context, before time.Time) (int, error) {
	return 0, f.step("email_changes", before)
}

func (f *fakeStore) PruneAudit(_ context.Context, before time.Time) (int, error) {
	return 3, f.step("audit", before)
}

type sent struct{ to, subject, text string }

type fakeMailer struct {
	mails []sent
	fail  bool
}

func (m *fakeMailer) SendText(_ context.Context, to, subject, text string) error {
	if m.fail {
		return errors.New("smtp down")
	}
	m.mails = append(m.mails, sent{to, subject, text})
	return nil
}

type limiter struct{ allow bool }

func (l limiter) Allow(string) bool { return l.allow }

func service(t *testing.T, store *fakeStore, mailer account.Mailer) *account.Service {
	t.Helper()
	svc, err := account.NewService(account.Deps{
		Store: store, Mailer: mailer, Policy: authz.NewPolicy(), Limiter: limiter{allow: true},
		Now: func() time.Time { return now }, Grace: 30 * 24 * time.Hour, AuditRetention: 730 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func fresh(user identity.UserID) access.Caller {
	return access.Caller{Principal: authz.Principal{User: user}, StepUpAt: now.Add(-time.Minute)}
}

func aged(user identity.UserID) access.Caller {
	return access.Caller{Principal: authz.Principal{User: user}, StepUpAt: now.Add(-16 * time.Minute)}
}

func TestNewService_GraceHasAFloor(t *testing.T) {
	_, err := account.NewService(account.Deps{Store: newStore(), Now: time.Now, Grace: 23 * time.Hour})
	if !errors.Is(err, account.ErrGraceTooShort) {
		t.Errorf("err = %v", err)
	}
	if _, err := account.NewService(account.Deps{Store: newStore(), Now: time.Now, Grace: 24 * time.Hour}); err != nil {
		t.Error(err)
	}
}

func TestSetDisplayName_NeedsAStepUpAndACleanName(t *testing.T) {
	store := newStore()
	svc := service(t, store, nil)
	ctx := context.Background()
	if err := svc.SetDisplayName(ctx, aged(alice), "Alice B."); !errors.Is(err, access.ErrStepUpRequired) {
		t.Errorf("aged step-up: %v", err)
	}
	for _, bad := range []string{"", "   ", "a\x00b", strings.Repeat("x", 201)} {
		if err := svc.SetDisplayName(ctx, fresh(alice), bad); !errors.Is(err, account.ErrInvalidName) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if err := svc.SetDisplayName(ctx, fresh(alice), "  Alice B.  "); err != nil {
		t.Fatal(err)
	}
	if got := store.profiles[alice].DisplayName; got != "Alice B." {
		t.Errorf("name = %q", got)
	}
	if a := store.audit[0]; a.Action != "account.rename" || a.Actor != alice || a.Target != "user:"+alice.String() || len(a.Details) != 0 {
		t.Errorf("audit = %+v", a)
	}
	if store.profiles[bob].DisplayName != "Bob" {
		t.Error("another account changed")
	}
}

// Without SMTP (development) the address applies at once.
func TestRequestEmailChange_WithoutMailAppliesAtOnce(t *testing.T) {
	store := newStore()
	svc := service(t, store, nil)
	ctx := context.Background()
	if _, err := svc.RequestEmailChange(ctx, aged(alice), "alice@example.net"); !errors.Is(err, access.ErrStepUpRequired) {
		t.Errorf("aged: %v", err)
	}
	for in, want := range map[string]error{
		"no-at-sign":        account.ErrInvalidEmail,
		"ALICE@example.org": account.ErrUnchanged,
		"bob@example.org":   account.ErrEmailTaken,
	} {
		if _, err := svc.RequestEmailChange(ctx, fresh(alice), in); !errors.Is(err, want) {
			t.Errorf("%s: %v, want %v", in, err, want)
		}
	}
	sent, err := svc.RequestEmailChange(ctx, fresh(alice), " Alice@Example.NET ")
	if err != nil || sent {
		t.Fatalf("sent %v, %v", sent, err)
	}
	if store.profiles[alice].Email != "alice@example.net" || store.audit[0].Action != "account.email_change" {
		t.Errorf("profile %+v, audit %+v", store.profiles[alice], store.audit)
	}
}

func codeOf(t *testing.T, m sent) string {
	t.Helper()
	_, rest, ok := strings.Cut(m.text, "Adresse: ")
	if !ok {
		t.Fatalf("no code in %q", m.text)
	}
	code, _, _ := strings.Cut(rest, "\n")
	return code
}

// With SMTP a code goes to the new address; only the code applies it, the old address hears of it.
func TestEmailChange_ConfirmedWithTheCodeFromTheNewMailbox(t *testing.T) {
	store := newStore()
	mailer := &fakeMailer{}
	svc := service(t, store, mailer)
	ctx := context.Background()
	sentCode, err := svc.RequestEmailChange(ctx, fresh(alice), "alice@example.net")
	if err != nil || !sentCode {
		t.Fatalf("sent %v, %v", sentCode, err)
	}
	if len(mailer.mails) != 1 || mailer.mails[0].to != "alice@example.net" {
		t.Fatalf("mails = %+v", mailer.mails)
	}
	code := codeOf(t, mailer.mails[0])
	if len(code) != 8 || store.profiles[alice].Email != "alice@example.org" {
		t.Fatalf("code %q, email %q", code, store.profiles[alice].Email)
	}
	pending := store.pending[alice]
	if strings.Contains(string(pending.CodeHash[:]), code) || !pending.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Errorf("pending = %+v", pending)
	}
	view, err := svc.Get(ctx, alice)
	if err != nil || view.Pending == nil || view.Pending.NewEmail != "alice@example.net" || !view.Verified {
		t.Errorf("view = %+v, %v", view, err)
	}
	// Another account can't use the code.
	store.pending[bob] = pending
	if err := svc.ConfirmEmailChange(ctx, bob, code); !errors.Is(err, account.ErrWrongCode) {
		t.Errorf("bob: %v", err)
	}
	if err := svc.ConfirmEmailChange(ctx, alice, "WRONGWRO"); !errors.Is(err, account.ErrWrongCode) {
		t.Errorf("wrong: %v", err)
	}
	typed := strings.ToLower(code[:4]) + "-" + code[4:] + " "
	if err := svc.ConfirmEmailChange(ctx, alice, typed); err != nil {
		t.Fatal(err)
	}
	if store.profiles[alice].Email != "alice@example.net" {
		t.Errorf("email = %q", store.profiles[alice].Email)
	}
	if len(mailer.mails) != 2 || mailer.mails[1].to != "alice@example.org" || strings.Contains(mailer.mails[1].text, "example.net") {
		t.Errorf("notice = %+v", mailer.mails)
	}
	if _, ok := store.pending[alice]; ok {
		t.Error("pending change kept")
	}
}

func TestEmailChange_FewAttemptsAndAnHour(t *testing.T) {
	ctx := context.Background()
	store := newStore()
	mailer := &fakeMailer{}
	svc := service(t, store, mailer)
	if _, err := svc.RequestEmailChange(ctx, fresh(alice), "alice@example.net"); err != nil {
		t.Fatal(err)
	}
	code := codeOf(t, mailer.mails[0])
	for i := range account.MaxCodeAttempts {
		if err := svc.ConfirmEmailChange(ctx, alice, "AAAAAAAA"); !errors.Is(err, account.ErrWrongCode) {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
	}
	if err := svc.ConfirmEmailChange(ctx, alice, code); !errors.Is(err, account.ErrNoPendingChange) {
		t.Errorf("after %d attempts: %v", account.MaxCodeAttempts, err)
	}
	if _, ok := store.pending[alice]; ok || store.profiles[alice].Email != "alice@example.org" {
		t.Error("change kept or applied")
	}

	if _, err := svc.RequestEmailChange(ctx, fresh(alice), "alice@example.net"); err != nil {
		t.Fatal(err)
	}
	p := store.pending[alice]
	p.ExpiresAt = now
	store.pending[alice] = p
	if err := svc.ConfirmEmailChange(ctx, alice, codeOf(t, mailer.mails[1])); !errors.Is(err, account.ErrNoPendingChange) {
		t.Errorf("expired: %v", err)
	}
	if view, _ := svc.Get(ctx, alice); view.Pending != nil {
		t.Error("expired change shown")
	}
	if err := svc.ConfirmEmailChange(ctx, bob, "AAAAAAAA"); !errors.Is(err, account.ErrNoPendingChange) {
		t.Errorf("none: %v", err)
	}
}

// The address of another account is only refused once the mailbox is proven: no lookup of
// addresses by asking for codes.
func TestEmailChange_TakenAddressRefusedAfterTheCode(t *testing.T) {
	ctx := context.Background()
	store := newStore()
	mailer := &fakeMailer{}
	svc := service(t, store, mailer)
	if sent, err := svc.RequestEmailChange(ctx, fresh(alice), "bob@example.org"); err != nil || !sent {
		t.Fatalf("%v, %v", sent, err)
	}
	if err := svc.ConfirmEmailChange(ctx, alice, codeOf(t, mailer.mails[0])); !errors.Is(err, account.ErrEmailTaken) {
		t.Errorf("err = %v", err)
	}
}

func TestRequestEmailChange_MailFailureAndLimit(t *testing.T) {
	ctx := context.Background()
	store := newStore()
	svc := service(t, store, &fakeMailer{fail: true})
	if _, err := svc.RequestEmailChange(ctx, fresh(alice), "alice@example.net"); !errors.Is(err, account.ErrMailFailed) {
		t.Errorf("err = %v", err)
	}
	if _, ok := store.pending[alice]; ok {
		t.Error("change without its mail kept")
	}
	limited, err := account.NewService(account.Deps{
		Store: store, Mailer: &fakeMailer{}, Policy: authz.NewPolicy(), Limiter: limiter{allow: false},
		Now: func() time.Time { return now }, Grace: 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := limited.RequestEmailChange(ctx, fresh(alice), "alice@example.net"); !errors.Is(err, account.ErrTooManyRequests) {
		t.Errorf("limited: %v", err)
	}
}

func TestDelete_OnlyTheSuperadminWithAFreshStepUp(t *testing.T) {
	ctx := context.Background()
	store := newStore()
	svc := service(t, store, nil)
	super := access.Caller{Principal: authz.Principal{User: sam, Superadmin: true}, StepUpAt: now.Add(-time.Minute)}
	admin := access.Caller{Principal: authz.Principal{
		User: sam, Org: tenant.OrgID(uuid.New()), Bindings: []authz.Binding{{AllGroups: true, Role: role.Admin}},
	}, StepUpAt: now}
	if _, err := svc.Delete(ctx, admin, "bob@example.org"); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("org admin: %v", err)
	}
	stale := super
	stale.StepUpAt = now.Add(-16 * time.Minute)
	if _, err := svc.Delete(ctx, stale, "bob@example.org"); !errors.Is(err, access.ErrStepUpRequired) {
		t.Errorf("aged: %v", err)
	}
	if _, err := svc.Delete(ctx, super, "nobody@example.org"); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
	if len(store.deleted) != 0 {
		t.Fatal("deleted without the right")
	}
	n, err := svc.Delete(ctx, super, " BOB@example.org")
	if err != nil || n != 2 || len(store.deleted) != 1 || store.deleted[0] != bob {
		t.Fatalf("n %d, %v, %v", n, err, store.deleted)
	}
	if a := store.audit[0]; a.Actor != sam || a.Action != "account.delete" || a.Target != "user:"+bob.String() || strings.Contains(a.Target, "@") {
		t.Errorf("audit = %+v", a)
	}
	store.deleteErr = access.ErrLastAdmin
	if _, err := svc.Delete(ctx, super, "alice@example.org"); !errors.Is(err, access.ErrLastAdmin) {
		t.Errorf("last admin: %v", err)
	}
}

func TestExport_TheCallersOwnDataWithAFreshStepUp(t *testing.T) {
	ctx := context.Background()
	store := newStore()
	svc := service(t, store, nil)
	if _, err := svc.Export(ctx, aged(alice)); !errors.Is(err, access.ErrStepUpRequired) {
		t.Errorf("aged: %v", err)
	}
	out, err := svc.Export(ctx, fresh(alice))
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]any
	if err := json.Unmarshal(out, &file); err != nil {
		t.Fatal(err)
	}
	acc, _ := file["account"].(map[string]any)
	if file["format"] != account.ExportFormat || file["exported_at"] != "2026-11-27T12:00:00Z" || acc["email"] != "alice@example.org" {
		t.Errorf("file = %v", file)
	}
	if strings.Contains(string(out), "bob") {
		t.Error("export names another account")
	}
	if a := store.audit[0]; a.Action != "account.export" || a.Actor != alice {
		t.Errorf("audit = %+v", a)
	}
}

func TestMaintain_PeriodsOfEveryStepAndNoStepStopsTheOthers(t *testing.T) {
	store := newStore()
	store.failStep = "accounts"
	svc := service(t, store, nil)
	if err := svc.Maintain(context.Background()); err == nil {
		t.Error("failure swallowed")
	}
	want := map[string]time.Time{
		"accounts":      now.Add(-30 * 24 * time.Hour),
		"invitations":   now,
		"email_changes": now,
		"audit":         now.Add(-730 * 24 * time.Hour),
	}
	for step, before := range want {
		if got, ok := store.cutoffs[step]; !ok || !got.Equal(before) {
			t.Errorf("%s: before %v, want %v", step, got, before)
		}
	}
}
