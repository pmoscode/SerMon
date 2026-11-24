//go:build integration

package webapi_test

import (
	"context"
	"encoding/base32"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/pmoscode/SerMon/server/internal/app/auth/totp"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
)

// enableTOTP sets up TOTP for the logged-in user and returns the secret and the recovery codes.
func enableTOTP(t *testing.T, l loggedIn) ([]byte, []string) {
	t.Helper()
	ctx := context.Background()
	begun, err := l.twoFactor.BeginTotpSetup(ctx, connect.NewRequest(&webv1.BeginTotpSetupRequest{}))
	if err != nil {
		t.Fatalf("BeginTotpSetup() = %v", err)
	}
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(begun.Msg.GetSecret())
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := l.twoFactor.ConfirmTotpSetup(ctx, connect.NewRequest(&webv1.ConfirmTotpSetupRequest{
		Code: totp.Code(secret, totp.Step(time.Now())),
	}))
	if err != nil || len(confirmed.Msg.GetRecoveryCodes()) != 10 {
		t.Fatalf("ConfirmTotpSetup() = %v, %v", confirmed, err)
	}
	return secret, confirmed.Msg.GetRecoveryCodes()
}

// nextCode is a valid code of a step after the ones already used (each step works once).
func nextCode(secret []byte) string { return totp.Code(secret, totp.Step(time.Now())+1) }

func TestTwoFactor_LoginNeedsTheCodeAndEveryCodeWorksOnce(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ctx := context.Background()
	mia := login(t, s, "mia@example.org")
	secret, recoveryCodes := enableTOTP(t, mia)
	if me, err := mia.auth.GetSession(ctx, connect.NewRequest(&webv1.GetSessionRequest{})); err != nil || !me.Msg.GetSession().GetSecondFactorEnabled() || !me.Msg.GetSession().GetTotpEnabled() || me.Msg.GetSession().GetPasskeyCount() != 0 {
		t.Fatalf("session after the setup = %v, %v", me, err)
	}
	if _, err := mia.twoFactor.BeginTotpSetup(ctx, connect.NewRequest(&webv1.BeginTotpSetupRequest{})); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Errorf("second setup: %v, want ALREADY_EXISTS", err)
	}

	// A new login stops before the second factor: no session, no API.
	again := login(t, s, "mia@example.org")
	if _, err := again.auth.GetSession(ctx, connect.NewRequest(&webv1.GetSessionRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("GetSession() while the code is missing: %v", err)
	}
	if _, err := again.system.GetVersion(ctx, connect.NewRequest(&webv1.GetVersionRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("API while the code is missing: %v", err)
	}
	if _, err := again.auth.VerifySecondFactor(ctx, connect.NewRequest(&webv1.VerifySecondFactorRequest{Code: "000000"})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("wrong code: %v", err)
	}
	verified, err := again.auth.VerifySecondFactor(ctx, connect.NewRequest(&webv1.VerifySecondFactorRequest{Code: nextCode(secret)}))
	if err != nil || verified.Msg.GetSession().GetEmail() != "mia@example.org" {
		t.Fatalf("VerifySecondFactor() = %v, %v", verified, err)
	}
	if _, err := again.system.GetVersion(ctx, connect.NewRequest(&webv1.GetVersionRequest{})); err != nil {
		t.Errorf("API after the code: %v", err)
	}

	// Step-up with the second factor, not the password; a recovery code works exactly once.
	if _, err := again.auth.ConfirmPassword(ctx, connect.NewRequest(&webv1.ConfirmPasswordRequest{Password: userPassword})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("password step-up with 2FA: %v", err)
	}
	if _, err := again.auth.ConfirmSecondFactor(ctx, connect.NewRequest(&webv1.ConfirmSecondFactorRequest{Code: recoveryCodes[0]})); err != nil {
		t.Errorf("step-up with a recovery code: %v", err)
	}
	if _, err := again.auth.ConfirmSecondFactor(ctx, connect.NewRequest(&webv1.ConfirmSecondFactorRequest{Code: recoveryCodes[0]})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("the same recovery code again: %v", err)
	}
	status, err := again.twoFactor.GetTwoFactorStatus(ctx, connect.NewRequest(&webv1.GetTwoFactorStatusRequest{}))
	if err != nil || !status.Msg.GetTotpEnabled() || status.Msg.GetRecoveryCodesLeft() != 9 {
		t.Errorf("status = %v, %v", status, err)
	}

	if _, err := again.twoFactor.DisableTwoFactor(ctx, connect.NewRequest(&webv1.DisableTwoFactorRequest{})); err != nil {
		t.Fatalf("DisableTwoFactor() = %v", err)
	}
	if res, err := mia.auth.Login(ctx, connect.NewRequest(&webv1.LoginRequest{Email: "mia@example.org", Password: userPassword})); err != nil || res.Msg.GetSecondFactorRequired() {
		t.Errorf("login after disabling: %v, %v", res, err)
	}
}

func TestTwoFactor_OrganizationsCanRequireIt(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	t.Cleanup(func() { seedAccessFixtures(t) })
	ctx := context.Background()
	ann := login(t, s, "ann@example.org") // admin of Alpha
	required := connect.NewRequest(&webv1.SetSecondFactorRequiredRequest{Required: true})

	if _, err := ann.policy.SetSecondFactorRequired(ctx, required); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("requiring it without an own second factor: %v", err)
	}
	enableTOTP(t, ann)
	if _, err := ann.policy.SetSecondFactorRequired(ctx, required); err != nil {
		t.Fatalf("SetSecondFactorRequired() = %v", err)
	}

	// Kai (member of Alpha and Beta) has no second factor: Alpha only allows the setup.
	kai := login(t, s, "kai@example.org")
	me, err := kai.auth.GetSession(ctx, connect.NewRequest(&webv1.GetSessionRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if me.Msg.GetSession().GetActiveOrganizationId() != orgAlpha {
		if _, err := kai.auth.SwitchOrganization(ctx, connect.NewRequest(&webv1.SwitchOrganizationRequest{OrganizationId: orgAlpha})); err != nil {
			t.Fatal(err)
		}
		me, _ = kai.auth.GetSession(ctx, connect.NewRequest(&webv1.GetSessionRequest{}))
	}
	if !me.Msg.GetSession().GetSecondFactorSetupRequired() {
		t.Errorf("session view = %v, want second_factor_setup_required", me.Msg.GetSession())
	}
	_, err = kai.system.GetVersion(ctx, connect.NewRequest(&webv1.GetVersionRequest{}))
	var connectErr *connect.Error
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !errors.As(err, &connectErr) || len(connectErr.Details()) != 1 {
		t.Errorf("API in Alpha without 2FA: %v, want FAILED_PRECONDITION with SecondFactorSetupRequired", err)
	}
	status, err := kai.twoFactor.GetTwoFactorStatus(ctx, connect.NewRequest(&webv1.GetTwoFactorStatusRequest{}))
	if err != nil || !status.Msg.GetRequiredByOrganization() {
		t.Errorf("the setup stays reachable: %v, %v", status, err)
	}
	// In Beta, which doesn't require it, Kai works as before.
	if _, err := kai.auth.SwitchOrganization(ctx, connect.NewRequest(&webv1.SwitchOrganizationRequest{OrganizationId: orgBeta})); err != nil {
		t.Fatal(err)
	}
	if _, err := kai.system.GetVersion(ctx, connect.NewRequest(&webv1.GetVersionRequest{})); err != nil {
		t.Errorf("API in Beta: %v", err)
	}

	// Beta's admin neither sees nor changes Alpha's requirement.
	bea := login(t, s, "bea@example.org")
	policy, err := bea.policy.GetSecurityPolicy(ctx, connect.NewRequest(&webv1.GetSecurityPolicyRequest{}))
	if err != nil || policy.Msg.GetSecondFactorRequired() {
		t.Errorf("Beta's policy = %v, %v", policy, err)
	}
	if _, err := bea.policy.SetSecondFactorRequired(ctx, connect.NewRequest(&webv1.SetSecondFactorRequiredRequest{})); err != nil {
		t.Errorf("Beta relaxes its own policy: %v", err)
	}
	if policy, err := ann.policy.GetSecurityPolicy(ctx, connect.NewRequest(&webv1.GetSecurityPolicyRequest{})); err != nil || !policy.Msg.GetSecondFactorRequired() {
		t.Errorf("Alpha's policy after Beta's change = %v, %v", policy, err)
	}
}
