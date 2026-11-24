//go:build integration

package webapi_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/pmoscode/SerMon/server/internal/app/auth/passkey/passkeytest"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

const (
	piaID   = "0193f5a0-0000-7000-8000-0000000000f1"
	orgPass = "0193f5a0-0000-7000-8000-0000000000f0"
)

// M3-23 over the real stack: Pia adds a passkey after the password, signs in with it without a
// password in a fresh browser, needs it as the second factor after the password, renews the
// step-up with it and satisfies her organization's two-factor requirement with it.
func TestPasskeys_RegisterSignInSecondFactorStepUp(t *testing.T) {
	s := newStack(t, 1000)
	ctx := context.Background()
	ownerSQL(t, "INSERT INTO organizations (id, name) VALUES ('"+orgPass+"', 'Passkeys') ON CONFLICT DO NOTHING")
	seedLogin(t, piaID, "pia@example.org", orgPass)
	ownerSQL(t, "DELETE FROM webauthn_credentials WHERE user_id = '"+piaID+"'",
		"UPDATE organizations SET require_second_factor = true WHERE id = '"+orgPass+"'")

	b := newBrowser(t, s)
	if opts, err := b.auth.GetLoginOptions(ctx, connect.NewRequest(&webv1.GetLoginOptionsRequest{})); err != nil || !opts.Msg.GetPasskeys() {
		t.Errorf("GetLoginOptions() = %v, %v", opts, err)
	}
	passkeys := webv1connect.NewPasskeyServiceClient(b.http, s.server.URL)
	if _, err := b.auth.Login(ctx, connect.NewRequest(&webv1.LoginRequest{Email: "pia@example.org", Password: userPassword})); err != nil {
		t.Fatal(err)
	}
	view, _ := b.auth.GetSession(ctx, connect.NewRequest(&webv1.GetSessionRequest{}))
	if !view.Msg.GetSession().GetSecondFactorSetupRequired() || !view.Msg.GetSession().GetPasskeysAvailable() {
		t.Fatalf("before: %v", view.Msg.GetSession())
	}
	// The organization waits for a second factor: adding a passkey works anyway (session only).
	begun, err := passkeys.BeginPasskeyRegistration(ctx, connect.NewRequest(&webv1.BeginPasskeyRegistrationRequest{Name: "Laptop"}))
	if err != nil {
		t.Fatal(err)
	}
	key := passkeytest.New(passkeyOrigin)
	answer, err := key.Register([]byte(begun.Msg.GetOptionsJson()))
	if err != nil {
		t.Fatal(err)
	}
	added, err := passkeys.FinishPasskeyRegistration(ctx, connect.NewRequest(&webv1.FinishPasskeyRegistrationRequest{
		CeremonyId: begun.Msg.GetCeremonyId(), CredentialJson: string(answer),
	}))
	if err != nil || added.Msg.GetPasskey().GetName() != "Laptop" {
		t.Fatalf("FinishPasskeyRegistration() = %v, %v", added, err)
	}
	view, _ = b.auth.GetSession(ctx, connect.NewRequest(&webv1.GetSessionRequest{}))
	if v := view.Msg.GetSession(); v.GetSecondFactorSetupRequired() || !v.GetSecondFactorEnabled() || v.GetTotpEnabled() || v.GetPasskeyCount() != 1 {
		t.Errorf("after: %v", v)
	}

	// A fresh browser signs in without a password.
	fresh := newBrowser(t, s)
	signIn := func(br browser) (*connect.Response[webv1.FinishPasskeyResponse], error) {
		lb, err := br.auth.BeginPasskey(ctx, connect.NewRequest(&webv1.BeginPasskeyRequest{Purpose: webv1.PasskeyPurpose_PASSKEY_PURPOSE_LOGIN}))
		if err != nil {
			t.Fatal(err)
		}
		answer, err := key.Assert([]byte(lb.Msg.GetOptionsJson()))
		if err != nil {
			t.Fatal(err)
		}
		return br.auth.FinishPasskey(ctx, connect.NewRequest(&webv1.FinishPasskeyRequest{
			Purpose: webv1.PasskeyPurpose_PASSKEY_PURPOSE_LOGIN, CeremonyId: lb.Msg.GetCeremonyId(), CredentialJson: string(answer),
		}))
	}
	res, err := signIn(fresh)
	if err != nil || res.Msg.GetSession().GetEmail() != "pia@example.org" || res.Msg.GetSession().GetSecondFactorSetupRequired() {
		t.Fatalf("sign-in = %v, %v", res, err)
	}
	if _, err := fresh.auth.GetSession(ctx, connect.NewRequest(&webv1.GetSessionRequest{})); err != nil {
		t.Errorf("no session after the sign-in: %v", err)
	}
	// The ceremony belongs to the browser that began it.
	lb, _ := fresh.auth.BeginPasskey(ctx, connect.NewRequest(&webv1.BeginPasskeyRequest{Purpose: webv1.PasskeyPurpose_PASSKEY_PURPOSE_LOGIN}))
	answer, _ = key.Assert([]byte(lb.Msg.GetOptionsJson()))
	stranger := newBrowser(t, s)
	if _, err := stranger.auth.FinishPasskey(ctx, connect.NewRequest(&webv1.FinishPasskeyRequest{
		Purpose: webv1.PasskeyPurpose_PASSKEY_PURPOSE_LOGIN, CeremonyId: lb.Msg.GetCeremonyId(), CredentialJson: string(answer),
	})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("another browser: %v", err)
	}

	// After the password the passkey is the second factor.
	pw := newBrowser(t, s)
	login, err := pw.auth.Login(ctx, connect.NewRequest(&webv1.LoginRequest{Email: "pia@example.org", Password: userPassword}))
	if err != nil || !login.Msg.GetSecondFactorRequired() {
		t.Fatalf("password login = %v, %v", login, err)
	}
	key.Verify = false // presence is enough as the second factor
	sb, err := pw.auth.BeginPasskey(ctx, connect.NewRequest(&webv1.BeginPasskeyRequest{Purpose: webv1.PasskeyPurpose_PASSKEY_PURPOSE_SECOND_FACTOR}))
	if err != nil {
		t.Fatal(err)
	}
	answer, _ = key.Assert([]byte(sb.Msg.GetOptionsJson()))
	second, err := pw.auth.FinishPasskey(ctx, connect.NewRequest(&webv1.FinishPasskeyRequest{
		Purpose: webv1.PasskeyPurpose_PASSKEY_PURPOSE_SECOND_FACTOR, CeremonyId: sb.Msg.GetCeremonyId(), CredentialJson: string(answer),
	}))
	if err != nil || second.Msg.GetSession().GetUserId() != piaID {
		t.Fatalf("second factor = %v, %v", second, err)
	}
	// Without user verification it is no sign-in without a password.
	if _, err := signIn(newBrowser(t, s)); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("sign-in without user verification: %v", err)
	}
	// The refusal counts towards the lockout of the account, like a wrong password.
	var failed int
	ownerQuery(t, "SELECT failed_attempts FROM user_credentials WHERE user_id = '"+piaID+"'", &failed)
	if failed != 1 {
		t.Errorf("%d failed attempts counted", failed)
	}

	// The step-up of the session.
	ownerSQL(t, "UPDATE sessions SET step_up_at = now() - interval '1 hour' WHERE user_id = '"+piaID+"'")
	pk := webv1connect.NewPasskeyServiceClient(pw.http, s.server.URL)
	if _, err := pk.BeginPasskeyRegistration(ctx, connect.NewRequest(&webv1.BeginPasskeyRegistrationRequest{Name: "Second"})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("registration with an old step-up: %v", err)
	}
	ub, _ := pw.auth.BeginPasskey(ctx, connect.NewRequest(&webv1.BeginPasskeyRequest{Purpose: webv1.PasskeyPurpose_PASSKEY_PURPOSE_STEP_UP}))
	answer, _ = key.Assert([]byte(ub.Msg.GetOptionsJson()))
	if _, err := pw.auth.FinishPasskey(ctx, connect.NewRequest(&webv1.FinishPasskeyRequest{
		Purpose: webv1.PasskeyPurpose_PASSKEY_PURPOSE_STEP_UP, CeremonyId: ub.Msg.GetCeremonyId(), CredentialJson: string(answer),
	})); err != nil {
		t.Fatalf("step-up: %v", err)
	}
	list, err := pk.ListPasskeys(ctx, connect.NewRequest(&webv1.ListPasskeysRequest{}))
	if err != nil || len(list.Msg.GetPasskeys()) != 1 || list.Msg.GetPasskeys()[0].GetLastUseTime() == nil {
		t.Fatalf("ListPasskeys() = %v, %v", list, err)
	}
	id := list.Msg.GetPasskeys()[0].GetId()
	if _, err := pk.RenamePasskey(ctx, connect.NewRequest(&webv1.RenamePasskeyRequest{Id: id, Name: "Work"})); err != nil {
		t.Errorf("RenamePasskey() = %v", err)
	}
	if _, err := pk.DeletePasskey(ctx, connect.NewRequest(&webv1.DeletePasskeyRequest{Id: id})); err != nil {
		t.Errorf("DeletePasskey() = %v", err)
	}
	// Without the passkey the organization waits for a second factor again.
	view, _ = pw.auth.GetSession(ctx, connect.NewRequest(&webv1.GetSessionRequest{}))
	if !view.Msg.GetSession().GetSecondFactorSetupRequired() {
		t.Errorf("after deleting the last second factor: %v", view.Msg.GetSession())
	}
	// M3-28: the session of the passkey sign-in had two factors and keeps them in its view.
	if v, err := fresh.auth.GetSession(ctx, connect.NewRequest(&webv1.GetSessionRequest{})); err != nil || v.Msg.GetSession().GetSecondFactorSetupRequired() {
		t.Errorf("the passkey sign-in's session: %v, %v", v, err)
	}
	var audits int
	ownerQuery(t, "SELECT count(*) FROM instance_audit_log WHERE target = 'user:"+piaID+"' AND action IN ('passkey.added', 'passkey.deleted')", &audits)
	if audits < 2 {
		t.Errorf("%d audit entries", audits)
	}
}
