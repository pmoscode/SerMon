//go:build integration

package webapi_test

import (
	"context"
	"net/http/cookiejar"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

const leaID = "0193f5a0-0000-7000-8000-0000000001d1" // member of Beta only

func anonymous(s stack) webv1connect.InvitationServiceClient {
	jar, _ := cookiejar.New(nil)
	hc := s.httpClient(jar, s.origin)
	return webv1connect.NewInvitationServiceClient(hc, s.server.URL)
}

func TestInvitationAPI_NewPersonAcceptsWithANewAccount(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ann := login(t, s, "ann@example.org")
	ctx := context.Background()
	email := "new-" + uuid.NewString()[:8] + "@example.org"

	invited, err := ann.access.InviteMember(ctx, connect.NewRequest(&webv1.InviteMemberRequest{Email: email, GroupId: groupAl, Role: webv1.Role_ROLE_OPERATOR}))
	if err != nil {
		t.Fatal(err)
	}
	token := invited.Msg.GetToken()
	if len(token) != 43 {
		t.Fatalf("token %q", token)
	}
	list, err := ann.access.ListInvitations(ctx, connect.NewRequest(&webv1.ListInvitationsRequest{}))
	if err != nil || !containsInvitation(list.Msg.GetInvitations(), invited.Msg.GetInvitation().GetId()) {
		t.Errorf("ListInvitations() lacks the new invitation: %v", err)
	}

	guest := anonymous(s)
	info, err := guest.GetInvitation(ctx, connect.NewRequest(&webv1.GetInvitationRequest{Token: token}))
	if err != nil || info.Msg.GetOrganizationName() != "Alpha" || info.Msg.GetEmail() != email || info.Msg.GetRole() != webv1.Role_ROLE_OPERATOR {
		t.Fatalf("GetInvitation() = %v, %v", info, err)
	}
	if _, err := guest.AcceptInvitation(ctx, connect.NewRequest(&webv1.AcceptInvitationRequest{Token: token, DisplayName: "New", Password: userPassword})); err != nil {
		t.Fatalf("AcceptInvitation() error = %v", err)
	}
	if _, err := guest.GetInvitation(ctx, connect.NewRequest(&webv1.GetInvitationRequest{Token: token})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("used link: %v, want UNAUTHENTICATED", err)
	}

	newcomer := login(t, s, email)
	groups, err := newcomer.access.ListNodeGroups(ctx, connect.NewRequest(&webv1.ListNodeGroupsRequest{}))
	if err != nil || len(groups.Msg.GetGroups()) != 1 || groups.Msg.GetGroups()[0].GetId() != groupAl {
		t.Errorf("newcomer sees %v, %v; want the invited group", groups, err)
	}
}

func TestInvitationAPI_ExistingUserJoinsAfterLogin(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	seedLogin(t, leaID, "lea@example.org", orgBeta)
	ownerSQL(t, "DELETE FROM memberships WHERE org_id = '"+orgAlpha+"' AND user_id = '"+leaID+"'")
	ann := login(t, s, "ann@example.org")
	ctx := context.Background()

	invited, err := ann.access.InviteMember(ctx, connect.NewRequest(&webv1.InviteMemberRequest{Email: "LEA@example.org", Role: webv1.Role_ROLE_VIEWER}))
	if err != nil {
		t.Fatal(err)
	}
	token := invited.Msg.GetToken()

	// Registering a second account for the address fails like an invalid link.
	if _, err := anonymous(s).AcceptInvitation(ctx, connect.NewRequest(&webv1.AcceptInvitationRequest{Token: token, DisplayName: "Fake", Password: userPassword})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("AcceptInvitation() for an existing address: %v, want UNAUTHENTICATED", err)
	}
	lea := login(t, s, "lea@example.org")
	joined, err := lea.invitation.JoinOrganization(ctx, connect.NewRequest(&webv1.JoinOrganizationRequest{Token: token}))
	if err != nil || joined.Msg.GetOrganizationId() != orgAlpha {
		t.Fatalf("JoinOrganization() = %v, %v", joined, err)
	}
	if _, err := lea.auth.SwitchOrganization(ctx, connect.NewRequest(&webv1.SwitchOrganizationRequest{OrganizationId: orgAlpha})); err != nil {
		t.Errorf("SwitchOrganization() into the joined organization: %v", err)
	}
}

func TestInvitationAPI_InvitingNeedsAStepUpAndRevokedLinksStopWorking(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ann := login(t, s, "ann@example.org")
	ctx := context.Background()

	invited, err := ann.access.InviteMember(ctx, connect.NewRequest(&webv1.InviteMemberRequest{Email: "gone@example.org", Role: webv1.Role_ROLE_VIEWER}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ann.access.RevokeInvitation(ctx, connect.NewRequest(&webv1.RevokeInvitationRequest{InvitationId: invited.Msg.GetInvitation().GetId()})); err != nil {
		t.Fatal(err)
	}
	if _, err := anonymous(s).GetInvitation(ctx, connect.NewRequest(&webv1.GetInvitationRequest{Token: invited.Msg.GetToken()})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("revoked link: %v, want UNAUTHENTICATED", err)
	}

	ownerSQL(t, "UPDATE sessions SET step_up_at = now() - interval '1 hour' WHERE user_id = '"+annID+"'")
	if _, err := ann.access.InviteMember(ctx, connect.NewRequest(&webv1.InviteMemberRequest{Email: "late@example.org", Role: webv1.Role_ROLE_VIEWER})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("InviteMember() with an old login: %v, want FAILED_PRECONDITION", err)
	}
}

func containsInvitation(invs []*webv1.Invitation, id string) bool {
	for _, i := range invs {
		if i.GetId() == id {
			return true
		}
	}
	return false
}
