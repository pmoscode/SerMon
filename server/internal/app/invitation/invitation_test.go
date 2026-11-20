package invitation_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/auth"
	"github.com/pmoscode/SerMon/server/internal/app/auth/password"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/invitation"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Test fixtures, not secrets.
const (
	newPassword   = "a very long passphrase 42" // betterleaks:allow
	shortPassword = "short"                     // betterleaks:allow
)

var (
	now   = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	ann   = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001"))
	bob   = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000002"))
	orgA  = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
	token = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" // 43 characters
)

type fakeStore struct {
	created   invitation.Invitation
	hash      [32]byte
	audits    []access.AuditEntry
	pending   *invitation.Pending
	account   invitation.NewAccount
	joined    identity.UserID
	acceptErr error
	calls     int
}

func (f *fakeStore) Create(_ context.Context, _ tenant.Scope, inv invitation.Invitation, hash [32]byte, a access.AuditEntry) error {
	f.calls++
	f.created, f.hash = inv, hash
	f.audits = append(f.audits, a)
	return nil
}

func (f *fakeStore) List(context.Context, tenant.Scope) ([]invitation.Invitation, error) {
	f.calls++
	return nil, nil
}

func (f *fakeStore) Revoke(_ context.Context, _ tenant.Scope, _ invitation.ID, a access.AuditEntry) error {
	f.calls++
	f.audits = append(f.audits, a)
	return nil
}

func (f *fakeStore) Lookup(_ context.Context, hash [32]byte, _ time.Time) (invitation.Pending, bool, error) {
	f.hash = hash
	if f.pending == nil {
		return invitation.Pending{}, false, nil
	}
	return *f.pending, true, nil
}

func (f *fakeStore) AcceptNewAccount(_ context.Context, _ invitation.Pending, a invitation.NewAccount, _ uuid.UUID, _ time.Time, audit access.AuditEntry) error {
	f.account = a
	f.audits = append(f.audits, audit)
	return f.acceptErr
}

func (f *fakeStore) AcceptExisting(_ context.Context, _ invitation.Pending, user identity.UserID, _ uuid.UUID, audit access.AuditEntry) error {
	f.joined = user
	f.audits = append(f.audits, audit)
	return f.acceptErr
}

type profiles map[identity.UserID]string

func (p profiles) Profile(_ context.Context, user identity.UserID) (auth.Profile, bool, error) {
	email, ok := p[user]
	return auth.Profile{Email: email, DisplayName: "X"}, ok, nil
}

type plainHasher struct{}

func (plainHasher) Hash(pw string) (string, error) { return "$argon2id$" + pw, nil }

func service(store *fakeStore, users profiles) *invitation.Service {
	return invitation.NewService(store, users, authz.NewPolicy(), plainHasher{}, func() time.Time { return now }, uuid.New)
}

func adminCaller(stepUp time.Duration) access.Caller {
	return access.Caller{
		Principal: authz.Principal{User: ann, Org: orgA, Bindings: []authz.Binding{{AllGroups: true, Role: role.Admin}}},
		StepUpAt:  now.Add(-stepUp),
	}
}

// pendingForBob is an open invitation of Org A for bob@example.org.
func pendingForBob() *invitation.Pending {
	return &invitation.Pending{ID: invitation.ID(uuid.New()), Org: orgA, OrgName: "Alpha", Email: "bob@example.org", Role: role.Operator, ExpiresAt: now.Add(time.Hour)}
}

func TestInvite_ReturnsATokenAndStoresOnlyItsHash(t *testing.T) {
	store := &fakeStore{}
	tok, inv, err := service(store, nil).Invite(context.Background(), adminCaller(time.Minute), " Bob@Example.org ", nil, role.Viewer)
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) != 43 || store.hash != sha256.Sum256([]byte(tok)) {
		t.Errorf("token %q / stored hash mismatch", tok)
	}
	if inv.Email != "bob@example.org" || inv.Role != role.Viewer || !inv.ExpiresAt.Equal(now.Add(invitation.Lifetime)) ||
		inv.InvitedBy == nil || *inv.InvitedBy != ann {
		t.Errorf("invitation = %+v", inv)
	}
	if a := store.audits[0]; a.Action != "member.invite" || a.Details["email"] != "bob@example.org" || a.Details["group"] != "*" {
		t.Errorf("audit = %+v", a)
	}
}

func TestInvite_RequiresAnOrgAdminWithStepUpAndValidInput(t *testing.T) {
	ctx := context.Background()
	viewer := access.Caller{Principal: authz.Principal{User: ann, Org: orgA, Bindings: []authz.Binding{{AllGroups: true, Role: role.Viewer}}}, StepUpAt: now}
	cases := map[string]struct {
		c     access.Caller
		email string
		r     role.Role
		want  error
	}{
		"viewer":        {viewer, "bob@example.org", role.Viewer, access.ErrForbidden},
		"stale step-up": {adminCaller(time.Hour), "bob@example.org", role.Viewer, access.ErrStepUpRequired},
		"bad email":     {adminCaller(time.Minute), "bob", role.Viewer, invitation.ErrInvalidEmail},
		"bad role":      {adminCaller(time.Minute), "bob@example.org", role.Role(0), access.ErrInvalidRole},
	}
	for name, tc := range cases {
		store := &fakeStore{}
		if _, _, err := service(store, nil).Invite(ctx, tc.c, tc.email, nil, tc.r); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
		if store.calls != 0 {
			t.Errorf("%s: store called", name)
		}
	}
	if err := service(&fakeStore{}, nil).Revoke(ctx, viewer, invitation.ID(uuid.New())); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("Revoke by viewer: %v", err)
	}
	if _, err := service(&fakeStore{}, nil).List(ctx, viewer); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("List by viewer: %v", err)
	}
}

func TestInspect_RejectsUnknownAndMalformedTokens(t *testing.T) {
	for _, tok := range []string{token, "short"} {
		if _, err := service(&fakeStore{}, nil).Inspect(context.Background(), tok); !errors.Is(err, invitation.ErrInvalidInvitation) {
			t.Errorf("Inspect(%q) = %v, want ErrInvalidInvitation", tok, err)
		}
	}
	p, err := service(&fakeStore{pending: pendingForBob()}, nil).Inspect(context.Background(), token)
	if err != nil || p.OrgName != "Alpha" {
		t.Errorf("Inspect(valid) = %+v, %v", p, err)
	}
}

func TestAcceptNewAccount_CreatesTheInvitedAddressWithAValidPassword(t *testing.T) {
	store := &fakeStore{pending: pendingForBob()}
	if _, err := service(store, nil).AcceptNewAccount(context.Background(), token, " Bob ", newPassword); err != nil {
		t.Fatal(err)
	}
	if store.account.DisplayName != "Bob" || store.account.PasswordHash != "$argon2id$"+newPassword || store.account.User.IsZero() {
		t.Errorf("account = %+v", store.account)
	}
	if a := store.audits[0]; a.Action != "member.join" || a.Actor != store.account.User {
		t.Errorf("audit = %+v", a)
	}

	weak := &fakeStore{pending: pendingForBob()}
	if _, err := service(weak, nil).AcceptNewAccount(context.Background(), token, "Bob", shortPassword); !errors.Is(err, password.ErrTooShort) {
		t.Errorf("weak password: %v", err)
	}
	taken := &fakeStore{pending: pendingForBob(), acceptErr: invitation.ErrInvalidInvitation}
	if _, err := service(taken, nil).AcceptNewAccount(context.Background(), token, "Bob", newPassword); !errors.Is(err, invitation.ErrInvalidInvitation) {
		t.Errorf("taken address: %v, want ErrInvalidInvitation", err)
	}
}

func TestJoin_OnlyTheInvitedAddressCanJoin(t *testing.T) {
	users := profiles{bob: "bob@example.org", ann: "ann@example.org"}
	store := &fakeStore{pending: pendingForBob()}

	if _, err := service(store, users).Join(context.Background(), token, ann); !errors.Is(err, invitation.ErrInvalidInvitation) {
		t.Errorf("Join by another user: %v, want ErrInvalidInvitation", err)
	}
	if store.joined != (identity.UserID{}) {
		t.Error("another user must not be added")
	}
	if _, err := service(store, users).Join(context.Background(), token, bob); err != nil || store.joined != bob {
		t.Errorf("Join by bob: %v (joined %v)", err, store.joined)
	}
}

func TestListAndRevoke_ByAnOrgAdmin(t *testing.T) {
	store := &fakeStore{}
	s := service(store, nil)
	if _, err := s.List(context.Background(), adminCaller(time.Hour)); err != nil {
		t.Errorf("List: %v", err)
	}
	id := invitation.ID(uuid.New())
	if err := s.Revoke(context.Background(), adminCaller(time.Hour), id); err != nil {
		t.Fatal(err)
	}
	if a := store.audits[0]; a.Action != "invitation.revoke" || a.Target != "invitation:"+id.String() || a.Actor != ann {
		t.Errorf("audit = %+v", a)
	}
}

func TestAcceptNewAccount_ValidatesTheName(t *testing.T) {
	for _, name := range []string{"", "  ", "a\x07b"} {
		store := &fakeStore{pending: pendingForBob()}
		if _, err := service(store, nil).AcceptNewAccount(context.Background(), token, name, newPassword); !errors.Is(err, invitation.ErrInvalidName) {
			t.Errorf("name %q: %v, want ErrInvalidName", name, err)
		}
	}
}

func TestJoin_UnknownUserIsRejected(t *testing.T) {
	store := &fakeStore{pending: pendingForBob()}
	if _, err := service(store, profiles{}).Join(context.Background(), token, bob); !errors.Is(err, invitation.ErrInvalidInvitation) {
		t.Errorf("error = %v, want ErrInvalidInvitation", err)
	}
}

type sentMail struct{ to, subject, text string }

type fakeMailer struct {
	sent []sentMail
	err  error
}

func (f *fakeMailer) SendText(_ context.Context, to, subject, text string) error {
	f.sent = append(f.sent, sentMail{to, subject, text})
	return f.err
}

// In production the link goes only to the invited address: whoever accepts owns the mailbox, so
// an admin can't occupy an account with someone else's address (M1-39). The API never sees it.
func TestInvite_WithAMailerSendsTheLinkOnlyByMail(t *testing.T) {
	store := &fakeStore{pending: pendingForBob()}
	mailer := &fakeMailer{}
	svc := invitation.NewService(store, profiles{ann: "ann@example.org"}, authz.NewPolicy(), plainHasher{}, func() time.Time { return now }, uuid.New,
		invitation.WithMailer(mailer, "https://sermon.example.com"))

	tok, _, err := svc.Invite(context.Background(), adminCaller(time.Minute), "bob@example.org", nil, role.Operator)
	if err != nil {
		t.Fatal(err)
	}
	if tok != "" {
		t.Errorf("token %q returned, want none (only the mailbox owner gets the link)", tok)
	}
	if len(mailer.sent) != 1 || mailer.sent[0].to != "bob@example.org" {
		t.Fatalf("sent = %+v, want one mail to bob", mailer.sent)
	}
	mail := mailer.sent[0]
	i := strings.Index(mail.text, "https://sermon.example.com/invite#")
	if i < 0 {
		t.Fatalf("mail text without link:\n%s", mail.text)
	}
	link := mail.text[i:]
	link = link[:strings.IndexAny(link+"\n", " \n")]
	if sum := sha256.Sum256([]byte(strings.TrimPrefix(link, "https://sermon.example.com/invite#"))); sum != store.hash {
		t.Errorf("the link in the mail doesn't match the stored hash: %s", link)
	}
	for _, want := range []string{"Alpha", "X", "operator"} {
		if !strings.Contains(mail.subject+mail.text, want) {
			t.Errorf("mail lacks %q (organization, inviter, role):\n%s\n%s", want, mail.subject, mail.text)
		}
	}

	mailer.err = errors.New("connection refused")
	if _, _, err := svc.Invite(context.Background(), adminCaller(time.Minute), "bob@example.org", nil, role.Operator); !errors.Is(err, invitation.ErrMailFailed) {
		t.Errorf("Invite() with a failing mailer = %v, want ErrMailFailed", err)
	}
}

func TestSendMail_ReadsWellWithoutInviterName(t *testing.T) {
	mailer := &fakeMailer{}
	if err := invitation.SendMail(context.Background(), mailer, "https://sermon.example.com/", "tok", invitation.MailFacts{
		To: "bea@example.org", Organization: "Beta", Role: role.Admin, ExpiresAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	text := mailer.sent[0].text
	for _, want := range []string{"Du wurdest zur Organisation „Beta“", "You have been invited to the organization \"Beta\"", "https://sermon.example.com/invite#tok"} {
		if !strings.Contains(text, want) {
			t.Errorf("mail lacks %q:\n%s", want, text)
		}
	}
}
