package webapi_test

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// isolationEnv is the setup of the cross-tenant integration test: the clients act as an admin
// of Org B (freshly logged in, so step-up is fresh); the IDs belong to Org A.
type isolationEnv struct {
	auth                            webv1connect.AuthServiceClient
	access                          webv1connect.AccessServiceClient
	invitation                      webv1connect.InvitationServiceClient
	organization                    webv1connect.OrganizationServiceClient
	enrollment                      webv1connect.EnrollmentTokenServiceClient
	orgA, groupA, memberA, bindingA string
	invitationA, inviteTokenA       string // an open invitation of Org A for another address
	enrollmentTokenA                string // an open enrollment token of Org A
}

// isolationCase calls one procedure with Org A's objects. Mutating calls must fail with
// NOT_FOUND or PERMISSION_DENIED; list calls must succeed without returning Org A's data
// (they return errLeak otherwise).
type isolationCase struct {
	list bool
	// tokenBound cases may also fail with UNAUTHENTICATED (invalid link for the caller).
	tokenBound bool
	run        func(ctx context.Context, e isolationEnv) error
}

var errLeak = errors.New("response contains data of another organization")

func leaks(e isolationEnv, s string) bool {
	return strings.Contains(s, e.groupA) || strings.Contains(s, e.memberA) || strings.Contains(s, e.bindingA) ||
		strings.Contains(s, e.orgA) || strings.Contains(s, e.invitationA) || strings.Contains(s, e.enrollmentTokenA)
}

// tenantIsolationCases maps every tenant RPC to its cross-tenant case (isolation_integration_test.go).
var tenantIsolationCases = map[string]isolationCase{
	"/sermon.web.v1.AuthService/SwitchOrganization": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.auth.SwitchOrganization(ctx, connect.NewRequest(&webv1.SwitchOrganizationRequest{OrganizationId: e.orgA}))
		return err
	}},
	"/sermon.web.v1.AccessService/ListNodeGroups": {list: true, run: func(ctx context.Context, e isolationEnv) error {
		res, err := e.access.ListNodeGroups(ctx, connect.NewRequest(&webv1.ListNodeGroupsRequest{}))
		if err == nil && leaks(e, res.Msg.String()) {
			return errLeak
		}
		return err
	}},
	"/sermon.web.v1.AccessService/CreateNodeGroup": {list: true, run: func(ctx context.Context, e isolationEnv) error {
		// Creating always acts in the caller's organization: the new group must not appear in Org A
		// (checked by the integration test after all cases).
		_, err := e.access.CreateNodeGroup(ctx, connect.NewRequest(&webv1.CreateNodeGroupRequest{Name: "created-by-b-" + uuid.NewString()[:8]}))
		return err
	}},
	"/sermon.web.v1.AccessService/RenameNodeGroup": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.access.RenameNodeGroup(ctx, connect.NewRequest(&webv1.RenameNodeGroupRequest{GroupId: e.groupA, Name: "pwned"}))
		return err
	}},
	"/sermon.web.v1.AccessService/DeleteNodeGroup": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.access.DeleteNodeGroup(ctx, connect.NewRequest(&webv1.DeleteNodeGroupRequest{GroupId: e.groupA}))
		return err
	}},
	"/sermon.web.v1.AccessService/ListMembers": {list: true, run: func(ctx context.Context, e isolationEnv) error {
		res, err := e.access.ListMembers(ctx, connect.NewRequest(&webv1.ListMembersRequest{}))
		if err == nil && leaks(e, res.Msg.String()) {
			return errLeak
		}
		return err
	}},
	"/sermon.web.v1.AccessService/GrantRole": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.access.GrantRole(ctx, connect.NewRequest(&webv1.GrantRoleRequest{UserId: e.memberA, Role: webv1.Role_ROLE_ADMIN}))
		return err
	}},
	"/sermon.web.v1.AccessService/RevokeRole": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.access.RevokeRole(ctx, connect.NewRequest(&webv1.RevokeRoleRequest{BindingId: e.bindingA}))
		return err
	}},
	"/sermon.web.v1.AccessService/RemoveMember": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.access.RemoveMember(ctx, connect.NewRequest(&webv1.RemoveMemberRequest{UserId: e.memberA}))
		return err
	}},
	"/sermon.web.v1.AccessService/InviteMember": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.access.InviteMember(ctx, connect.NewRequest(&webv1.InviteMemberRequest{
			Email: "x-" + uuid.NewString()[:8] + "@example.org", GroupId: e.groupA, Role: webv1.Role_ROLE_ADMIN,
		}))
		return err
	}},
	"/sermon.web.v1.AccessService/ListInvitations": {list: true, run: func(ctx context.Context, e isolationEnv) error {
		res, err := e.access.ListInvitations(ctx, connect.NewRequest(&webv1.ListInvitationsRequest{}))
		if err == nil && (leaks(e, res.Msg.String()) || strings.Contains(res.Msg.String(), "invited-a@")) {
			return errLeak
		}
		return err
	}},
	"/sermon.web.v1.AccessService/RevokeInvitation": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.access.RevokeInvitation(ctx, connect.NewRequest(&webv1.RevokeInvitationRequest{InvitationId: e.invitationA}))
		return err
	}},
	"/sermon.web.v1.InvitationService/JoinOrganization": {tokenBound: true, run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.invitation.JoinOrganization(ctx, connect.NewRequest(&webv1.JoinOrganizationRequest{Token: e.inviteTokenA}))
		return err
	}},
	// Instance-level RPCs: an org admin is not a superadmin.
	"/sermon.web.v1.OrganizationService/ListOrganizations": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.organization.ListOrganizations(ctx, connect.NewRequest(&webv1.ListOrganizationsRequest{}))
		return err
	}},
	"/sermon.web.v1.OrganizationService/CreateOrganization": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.organization.CreateOrganization(ctx, connect.NewRequest(&webv1.CreateOrganizationRequest{Name: "rogue", AdminEmail: "rogue@example.org"}))
		return err
	}},
	"/sermon.web.v1.OrganizationService/RenameOrganization": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.organization.RenameOrganization(ctx, connect.NewRequest(&webv1.RenameOrganizationRequest{OrganizationId: e.orgA, Name: "pwned"}))
		return err
	}},
	"/sermon.web.v1.EnrollmentTokenService/CreateEnrollmentToken": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.enrollment.CreateEnrollmentToken(ctx, connect.NewRequest(&webv1.CreateEnrollmentTokenRequest{GroupId: e.groupA, MaxUses: 1000}))
		return err
	}},
	"/sermon.web.v1.EnrollmentTokenService/ListEnrollmentTokens": {list: true, run: func(ctx context.Context, e isolationEnv) error {
		res, err := e.enrollment.ListEnrollmentTokens(ctx, connect.NewRequest(&webv1.ListEnrollmentTokensRequest{}))
		if err == nil && leaks(e, res.Msg.String()) {
			return errLeak
		}
		return err
	}},
	"/sermon.web.v1.EnrollmentTokenService/RevokeEnrollmentToken": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.enrollment.RevokeEnrollmentToken(ctx, connect.NewRequest(&webv1.RevokeEnrollmentTokenRequest{TokenId: e.enrollmentTokenA}))
		return err
	}},
	"/sermon.web.v1.AccessService/ListAuditLog": {list: true, run: func(ctx context.Context, e isolationEnv) error {
		res, err := e.access.ListAuditLog(ctx, connect.NewRequest(&webv1.ListAuditLogRequest{}))
		if err == nil && (leaks(e, res.Msg.String()) || strings.Contains(res.Msg.String(), "by-a")) {
			return errLeak
		}
		return err
	}},
}
