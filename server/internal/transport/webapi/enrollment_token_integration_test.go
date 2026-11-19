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
