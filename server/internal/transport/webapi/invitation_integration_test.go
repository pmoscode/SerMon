//go:build integration

package webapi_test

import (
	"context"
	"errors"
	"net/http/cookiejar"
	"strings"
	"sync"
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
	late := connect.NewRequest(&webv1.InviteMemberRequest{Email: "late@example.org", Role: webv1.Role_ROLE_VIEWER})
	_, err = ann.access.InviteMember(ctx, late)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !hasStepUpDetail(err) {
		t.Errorf("InviteMember() with an old login: %v, want FAILED_PRECONDITION with StepUpRequired", err)
	}

	if _, err := ann.auth.ConfirmPassword(ctx, connect.NewRequest(&webv1.ConfirmPasswordRequest{Password: userPassword})); err != nil {
		t.Fatalf("ConfirmPassword() error = %v", err)
	}
	if _, err := ann.access.InviteMember(ctx, late); err != nil {
		t.Errorf("InviteMember() after the step-up: %v", err)
	}
}

func hasStepUpDetail(err error) bool {
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		return false
	}
	for _, detail := range connectErr.Details() {
		if msg, err := detail.Value(); err == nil {
			if _, ok := msg.(*webv1.StepUpRequired); ok {
				return true
			}
		}
	}
	return false
}

func containsInvitation(invs []*webv1.Invitation, id string) bool {
	for _, i := range invs {
		if i.GetId() == id {
			return true
		}
	}
	return false
}

type recordingMailer struct {
	mu   sync.Mutex
	sent []string // "to: text"
	err  error
}

func (m *recordingMailer) SendText(_ context.Context, to, _, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, to+": "+text)
	return m.err
}

// In production the API never hands out the link (M1-39): the admin can't accept it under the
// invited address; the mailed link works. A failing mail server is UNAVAILABLE, nothing more.
func TestInvitationAPI_WithMailTheLinkOnlyReachesTheInvitedAddress(t *testing.T) {
	mailer := &recordingMailer{}
	s := newStackWithMailer(t, 1000, mailer)
	seedAccessFixtures(t)
	ann := login(t, s, "ann@example.org")
	ctx := context.Background()

	res, err := ann.access.InviteMember(ctx, connect.NewRequest(&webv1.InviteMemberRequest{Email: "mailed@example.org", Role: webv1.Role_ROLE_VIEWER}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Msg.GetToken() != "" || !res.Msg.GetEmailed() {
		t.Fatalf("response = %v, want emailed without token", res.Msg)
	}
	mailer.mu.Lock()
	sent := strings.Join(mailer.sent, "\n")
	mailer.mu.Unlock()
	i := strings.Index(sent, "/invite#")
	if !strings.HasPrefix(sent, "mailed@example.org: ") || i < 0 {
		t.Fatalf("mails = %q", sent)
	}
	token, _, _ := strings.Cut(sent[i+len("/invite#"):], "\n")
	if _, err := anonymous(s).GetInvitation(ctx, connect.NewRequest(&webv1.GetInvitationRequest{Token: token})); err != nil {
		t.Errorf("the mailed link doesn't work: %v", err)
	}

	mailer.mu.Lock()
	mailer.err = errors.New("dial tcp: connection refused to mail.internal")
	mailer.mu.Unlock()
	_, err = ann.access.InviteMember(ctx, connect.NewRequest(&webv1.InviteMemberRequest{Email: "other@example.org", Role: webv1.Role_ROLE_VIEWER}))
	if connect.CodeOf(err) != connect.CodeUnavailable || strings.Contains(err.Error(), "mail.internal") {
		t.Errorf("failing mail: %v, want UNAVAILABLE without details", err)
	}
}
