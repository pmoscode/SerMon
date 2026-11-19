//go:build integration

package webapi_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
)

const samID = "0193f5a0-0000-7000-8000-0000000001e1" // superadmin without any membership

func TestOrganizationAPI_SuperadminCreatesAnOrganizationAndItsAdminJoins(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	seedLogin(t, samID, "sam@example.org")
	ownerSQL(t, "INSERT INTO superadmins (user_id) VALUES ('"+samID+"') ON CONFLICT DO NOTHING")
	sam := login(t, s, "sam@example.org")
	ctx := context.Background()

	list, err := sam.organization.ListOrganizations(ctx, connect.NewRequest(&webv1.ListOrganizationsRequest{}))
	if err != nil || len(list.Msg.GetOrganizations()) < 2 {
		t.Fatalf("ListOrganizations() = %v, %v", list, err)
	}
	// Without a membership the superadmin sees no tenant data.
	if _, err := sam.access.ListMembers(ctx, connect.NewRequest(&webv1.ListMembersRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("ListMembers() by the superadmin: %v, want PERMISSION_DENIED", err)
	}

	adminEmail := "gamma-admin-" + uuid.NewString()[:8] + "@example.org"
	created, err := sam.organization.CreateOrganization(ctx, connect.NewRequest(&webv1.CreateOrganizationRequest{Name: "Gamma", AdminEmail: adminEmail}))
	if err != nil {
		t.Fatal(err)
	}
	org := created.Msg.GetOrganization().GetId()
	if _, err := sam.organization.RenameOrganization(ctx, connect.NewRequest(&webv1.RenameOrganizationRequest{OrganizationId: org, Name: "Gamma Ltd"})); err != nil {
		t.Fatal(err)
	}
	if _, err := sam.organization.RenameOrganization(ctx, connect.NewRequest(&webv1.RenameOrganizationRequest{OrganizationId: uuid.NewString(), Name: "x"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("rename unknown: %v, want NOT_FOUND", err)
	}

	// The first admin accepts the invitation and manages the new organization.
	guest := anonymous(s)
	if _, err := guest.AcceptInvitation(ctx, connect.NewRequest(&webv1.AcceptInvitationRequest{
		Token: created.Msg.GetInvitationToken(), DisplayName: "Gamma Admin", Password: userPassword,
	})); err != nil {
		t.Fatalf("AcceptInvitation() error = %v", err)
	}
	admin := login(t, s, adminEmail)
	members, err := admin.access.ListMembers(ctx, connect.NewRequest(&webv1.ListMembersRequest{}))
	if err != nil || len(members.Msg.GetMembers()) != 1 || members.Msg.GetMembers()[0].GetBindings()[0].GetRole() != webv1.Role_ROLE_ADMIN {
		t.Errorf("members of Gamma = %v, %v", members, err)
	}
	audit, err := admin.access.ListAuditLog(ctx, connect.NewRequest(&webv1.ListAuditLogRequest{}))
	var actions []string
	for _, e := range audit.Msg.GetEntries() {
		actions = append(actions, e.GetAction())
	}
	if err != nil || len(actions) != 3 || actions[0] != "member.join" || actions[1] != "org.rename" || actions[2] != "org.create" {
		t.Errorf("audit of Gamma = %v, %v", actions, err)
	}

	// Creating needs a step-up.
	ownerSQL(t, "UPDATE sessions SET step_up_at = now() - interval '1 hour' WHERE user_id = '"+samID+"'")
	if _, err := sam.organization.CreateOrganization(ctx, connect.NewRequest(&webv1.CreateOrganizationRequest{Name: "Late", AdminEmail: "late@example.org"})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("create with an old login: %v, want FAILED_PRECONDITION", err)
	}
}
