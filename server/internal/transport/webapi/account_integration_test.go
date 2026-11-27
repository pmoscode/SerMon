//go:build integration

package webapi_test

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
)

// eveFor seeds a member of Alpha of the test's own (no other test uses the address).
func eveFor(t *testing.T) (id, email string) {
	t.Helper()
	id = uuid.NewString()
	email = "eve-" + id[:8] + "@example.org"
	seedLogin(t, id, email, orgAlpha)
	t.Cleanup(func() { ownerSQL(t, "DELETE FROM users WHERE id = '"+id+"'") })
	return id, email
}

func mailedCode(t *testing.T, m *recordingMailer, to string) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, sent := range m.sent {
		if rest, ok := strings.CutPrefix(sent, to+": "); ok {
			if _, after, found := strings.Cut(rest, "Adresse: "); found {
				code, _, _ := strings.Cut(after, "\n")
				return code
			}
		}
	}
	t.Fatalf("no code mailed to %s: %v", to, m.sent)
	return ""
}

// V1-17: name and address of the own account; a new address needs the code from its mailbox and
// becomes the login.
func TestAccountAPI_ChangesTheOwnNameAndAddress(t *testing.T) {
	mailer := &recordingMailer{}
	s := newStackWithMailer(t, 1000, mailer)
	seedAccessFixtures(t)
	_, email := eveFor(t)
	eve := login(t, s, email)
	ctx := context.Background()

	if _, err := eve.account.SetDisplayName(ctx, connect.NewRequest(&webv1.SetDisplayNameRequest{DisplayName: "Eve E."})); err != nil {
		t.Fatal(err)
	}
	newEmail := strings.Replace(email, "@example.org", "@example.net", 1)
	res, err := eve.account.RequestEmailChange(ctx, connect.NewRequest(&webv1.RequestEmailChangeRequest{Email: newEmail}))
	if err != nil || !res.Msg.GetCodeSent() {
		t.Fatalf("request: %v, %v", res, err)
	}
	got, err := eve.account.GetAccount(ctx, connect.NewRequest(&webv1.GetAccountRequest{}))
	if err != nil || got.Msg.GetEmail() != email || got.Msg.GetPendingEmail() != newEmail || got.Msg.GetDisplayName() != "Eve E." || !got.Msg.GetEmailVerification() {
		t.Fatalf("account = %v, %v", got, err)
	}
	if _, err := eve.account.ConfirmEmailChange(ctx, connect.NewRequest(&webv1.ConfirmEmailChangeRequest{Code: "WRONGWRO"})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("wrong code: %v", err)
	}
	// Another member can't confirm with the code: it is bound to Eve's account.
	code := mailedCode(t, mailer, newEmail)
	mia := login(t, s, "mia@example.org")
	if _, err := mia.account.ConfirmEmailChange(ctx, connect.NewRequest(&webv1.ConfirmEmailChangeRequest{Code: code})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("mia: %v", err)
	}
	if _, err := eve.account.ConfirmEmailChange(ctx, connect.NewRequest(&webv1.ConfirmEmailChangeRequest{Code: code})); err != nil {
		t.Fatal(err)
	}
	if _, err := eve.auth.Login(ctx, connect.NewRequest(&webv1.LoginRequest{Email: newEmail, Password: userPassword})); err != nil {
		t.Errorf("login with the new address: %v", err)
	}
	if _, err := eve.auth.Login(ctx, connect.NewRequest(&webv1.LoginRequest{Email: email, Password: userPassword})); err == nil {
		t.Error("the old address still logs in")
	}
}

// Art. 15: the export holds Eve's data, nothing of other members or organizations.
func TestAccountAPI_ExportHoldsNothingOfOthers(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	id, email := eveFor(t)
	eve := login(t, s, email)
	res, err := eve.account.ExportAccountData(context.Background(), connect.NewRequest(&webv1.ExportAccountDataRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	out := res.Msg.GetJson()
	for _, want := range []string{id, email, `"organization": "Alpha"`, "sermon-account-export/1"} {
		if !strings.Contains(out, want) {
			t.Errorf("export lacks %q", want)
		}
	}
	for _, foreign := range []string{"ann@example.org", "mia@example.org", annID, miaID, "Beta", "argon2id"} {
		if strings.Contains(out, foreign) {
			t.Errorf("export contains %q", foreign)
		}
	}
}

// A superadmin deletes an account on request; an org admin can't.
func TestAccountAPI_TheSuperadminDeletesAnAccount(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	seedLogin(t, samID, "sam@example.org")
	ownerSQL(t, "INSERT INTO superadmins (user_id) VALUES ('"+samID+"') ON CONFLICT DO NOTHING")
	id, email := eveFor(t)
	ann, sam, eve := login(t, s, "ann@example.org"), login(t, s, "sam@example.org"), login(t, s, email)
	ctx := context.Background()
	del := func(c loggedIn, address string) (*connect.Response[webv1.DeleteAccountResponse], error) {
		return c.accountAdmin.DeleteAccount(ctx, connect.NewRequest(&webv1.DeleteAccountRequest{Email: address}))
	}
	if _, err := del(ann, email); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("org admin: %v", err)
	}
	if _, err := del(sam, "nobody@example.org"); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("unknown: %v", err)
	}
	if _, err := del(sam, "sam@example.org"); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("superadmin: %v", err)
	}
	res, err := del(sam, strings.ToUpper(email))
	if err != nil || res.Msg.GetMemberships() != 1 {
		t.Fatalf("delete: %v, %v", res, err)
	}
	// Her open session ends with the account.
	if _, err := eve.account.GetAccount(ctx, connect.NewRequest(&webv1.GetAccountRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("session after the deletion: %v", err)
	}
	var left int
	ownerQueryRow(t, "SELECT count(*) FROM users WHERE id = '"+id+"'", &left)
	if left != 0 {
		t.Error("account left")
	}
	var entries int
	ownerQueryRow(t, "SELECT count(*) FROM audit_log WHERE org_id = '"+orgAlpha+"' AND action = 'member.account_deleted' AND target = 'user:"+id+"' AND actor_id = '"+samID+"'", &entries)
	if entries != 1 {
		t.Errorf("audit entries in Alpha: %d", entries)
	}
}
