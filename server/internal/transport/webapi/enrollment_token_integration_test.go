//go:build integration

package webapi_test

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"

	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
)

func TestEnrollmentTokenAPI_OperatorsIssueListAndRevokeTokens(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ann := login(t, s, "ann@example.org")
	ctx := context.Background()

	created, err := ann.enrollment.CreateEnrollmentToken(ctx, connect.NewRequest(&webv1.CreateEnrollmentTokenRequest{GroupId: groupAl, TtlSeconds: 600, MaxUses: 5}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Msg.GetSecret(), "sme_") || created.Msg.GetToken().GetMaxUses() != 5 {
		t.Errorf("created = %v", created.Msg)
	}
	list, err := ann.enrollment.ListEnrollmentTokens(ctx, connect.NewRequest(&webv1.ListEnrollmentTokensRequest{}))
	if err != nil || !strings.Contains(list.Msg.String(), created.Msg.GetToken().GetId()) {
		t.Errorf("list lacks the new token: %v", err)
	}
	if strings.Contains(list.Msg.String(), created.Msg.GetSecret()) {
		t.Error("the secret must never be listed")
	}
	if _, err := ann.enrollment.RevokeEnrollmentToken(ctx, connect.NewRequest(&webv1.RevokeEnrollmentTokenRequest{TokenId: created.Msg.GetToken().GetId()})); err != nil {
		t.Fatal(err)
	}
	if _, err := ann.enrollment.CreateEnrollmentToken(ctx, connect.NewRequest(&webv1.CreateEnrollmentTokenRequest{GroupId: groupAl, TtlSeconds: 60})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("ttl of 60 s: %v, want INVALID_ARGUMENT", err)
	}

	mia := login(t, s, "mia@example.org") // viewer on the group
	if _, err := mia.enrollment.CreateEnrollmentToken(ctx, connect.NewRequest(&webv1.CreateEnrollmentTokenRequest{GroupId: groupAl})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("viewer: %v, want PERMISSION_DENIED", err)
	}
	if res, err := mia.enrollment.ListEnrollmentTokens(ctx, connect.NewRequest(&webv1.ListEnrollmentTokensRequest{})); err != nil || len(res.Msg.GetTokens()) != 0 {
		t.Errorf("viewer list = %v, %v; want empty", res, err)
	}
}

// A hijacked session must not let machines join: creating a token needs a recent step-up.
func TestEnrollmentTokenAPI_CreatingNeedsAStepUp(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ann := login(t, s, "ann@example.org")
	ctx := context.Background()

	ownerSQL(t, "UPDATE sessions SET step_up_at = now() - interval '1 hour' WHERE user_id = '"+annID+"'")
	req := connect.NewRequest(&webv1.CreateEnrollmentTokenRequest{GroupId: groupAl})
	if _, err := ann.enrollment.CreateEnrollmentToken(ctx, req); connect.CodeOf(err) != connect.CodeFailedPrecondition || !hasStepUpDetail(err) {
		t.Errorf("CreateEnrollmentToken() with an old login: %v, want FAILED_PRECONDITION with StepUpRequired", err)
	}
	if _, err := ann.auth.ConfirmPassword(ctx, connect.NewRequest(&webv1.ConfirmPasswordRequest{Password: userPassword})); err != nil {
		t.Fatalf("ConfirmPassword() error = %v", err)
	}
	if _, err := ann.enrollment.CreateEnrollmentToken(ctx, req); err != nil {
		t.Errorf("CreateEnrollmentToken() after the step-up: %v", err)
	}
}
