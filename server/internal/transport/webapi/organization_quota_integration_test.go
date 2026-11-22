//go:build integration

package webapi_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
)

func u32(v uint32) *uint32 { return &v }

// superadmin logs in sam, a superadmin without any membership.
func superadmin(t *testing.T, s stack) loggedIn {
	t.Helper()
	seedLogin(t, samID, "sam@example.org")
	ownerSQL(t, "INSERT INTO superadmins (user_id) VALUES ('"+samID+"') ON CONFLICT DO NOTHING")
	return login(t, s, "sam@example.org")
}

// setQuotas sets name and quotas of an organization as the superadmin.
func setQuotas(t *testing.T, sam loggedIn, org, name string, q *webv1.OrganizationQuotas) {
	t.Helper()
	if _, err := sam.organization.UpdateOrganization(context.Background(), connect.NewRequest(&webv1.UpdateOrganizationRequest{
		OrganizationId: org, Name: name, Quotas: q,
	})); err != nil {
		t.Fatal(err)
	}
}

// M2-32: the superadmin sets name and quotas with a step-up; names are unique regardless of
// case; the audit entry lands in the organization's log; org admins can't.
func TestOrganizationAPI_UpdateSetsQuotasAndNamesAreUnique(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	sam := superadmin(t, s)
	ctx := context.Background()
	suffix := uuid.NewString()[:8]
	name := "Delta " + suffix

	created, err := sam.organization.CreateOrganization(ctx, connect.NewRequest(&webv1.CreateOrganizationRequest{Name: name, AdminEmail: "delta-" + suffix + "@example.org"}))
	if err != nil {
		t.Fatal(err)
	}
	org := created.Msg.GetOrganization().GetId()
	if _, err := sam.organization.CreateOrganization(ctx, connect.NewRequest(&webv1.CreateOrganizationRequest{Name: strings.ToUpper(name), AdminEmail: "x-" + suffix + "@example.org"})); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Errorf("create with the name in other case: %v, want ALREADY_EXISTS", err)
	}
	if _, err := sam.organization.RenameOrganization(ctx, connect.NewRequest(&webv1.RenameOrganizationRequest{OrganizationId: orgBeta, Name: strings.ToLower(name)})); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Errorf("rename to a taken name: %v, want ALREADY_EXISTS", err)
	}

	q := &webv1.OrganizationQuotas{MaxNodes: u32(20), MaxMembers: u32(5), RetentionCapDays: u32(90), SamplesPerSecond: u32(2000), SeriesPerNode: u32(500)}
	res, err := sam.organization.UpdateOrganization(ctx, connect.NewRequest(&webv1.UpdateOrganizationRequest{OrganizationId: org, Name: name + " AG", Quotas: q}))
	if err != nil {
		t.Fatal(err)
	}
	got := res.Msg.GetOrganization()
	if got.GetName() != name+" AG" || got.GetQuotas().GetMaxNodes() != 20 || got.GetQuotas().GetSeriesPerNode() != 500 ||
		got.GetStatus() != webv1.OrganizationStatus_ORGANIZATION_STATUS_ACTIVE {
		t.Errorf("updated = %v", got)
	}
	var quotas, previous string
	ownerQueryRow(t, "SELECT details->>'quotas', details->>'previous_quotas' FROM audit_log WHERE org_id = '"+org+"' AND action = 'org.update' ORDER BY seq DESC LIMIT 1", &quotas, &previous)
	if quotas != "max_nodes=20,max_members=5,retention_cap_days=90,samples_per_second=2000,series_per_node=500" || previous != "" {
		t.Errorf("audit quotas=%q previous=%q", quotas, previous)
	}
	// Back to the instance defaults: no quotas set.
	setQuotas(t, sam, org, name, &webv1.OrganizationQuotas{})
	list, err := sam.organization.ListOrganizations(ctx, connect.NewRequest(&webv1.ListOrganizationsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range list.Msg.GetOrganizations() {
		if o.GetId() == org && (o.GetQuotas().MaxNodes != nil || o.GetName() != name) {
			t.Errorf("after the reset: %v", o)
		}
	}

	if _, err := sam.organization.UpdateOrganization(ctx, connect.NewRequest(&webv1.UpdateOrganizationRequest{OrganizationId: org, Name: name, Quotas: &webv1.OrganizationQuotas{MaxNodes: u32(0)}})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("quota 0: %v, want INVALID_ARGUMENT", err)
	}
	if _, err := sam.organization.UpdateOrganization(ctx, connect.NewRequest(&webv1.UpdateOrganizationRequest{OrganizationId: orgBeta, Name: name, Quotas: &webv1.OrganizationQuotas{}})); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Errorf("update to a taken name: %v, want ALREADY_EXISTS", err)
	}
	ann := login(t, s, "ann@example.org")
	if _, err := ann.organization.UpdateOrganization(ctx, connect.NewRequest(&webv1.UpdateOrganizationRequest{OrganizationId: orgAlpha, Name: "Alpha", Quotas: &webv1.OrganizationQuotas{}})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("org admin: %v, want PERMISSION_DENIED", err)
	}
	ownerSQL(t, "UPDATE sessions SET step_up_at = now() - interval '1 hour' WHERE user_id = '"+samID+"'")
	if _, err := sam.organization.UpdateOrganization(ctx, connect.NewRequest(&webv1.UpdateOrganizationRequest{OrganizationId: org, Name: name, Quotas: &webv1.OrganizationQuotas{}})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("old login: %v, want FAILED_PRECONDITION", err)
	}
}

// The list counts nodes and estimates storage from the instance's sizes: an organization with
// series has a share, one without has none. No names of nodes or users in it.
func TestOrganizationAPI_ListCountsNodesAndEstimatesStorage(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	sam := superadmin(t, s)
	ctx := context.Background()
	ownerSQL(t, "SELECT set_config('sermon.org_id', '"+orgAlpha+"', false)",
		"INSERT INTO series (org_id, node_id, metric) VALUES ('"+orgAlpha+"', '"+nodeAl+"', 'sermon_quota_estimate_test') ON CONFLICT DO NOTHING")
	suffix := uuid.NewString()[:8]
	empty, err := sam.organization.CreateOrganization(ctx, connect.NewRequest(&webv1.CreateOrganizationRequest{Name: "Empty " + suffix, AdminEmail: "empty-" + suffix + "@example.org"}))
	if err != nil {
		t.Fatal(err)
	}
	list, err := sam.organization.ListOrganizations(ctx, connect.NewRequest(&webv1.ListOrganizationsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	var alpha, none *webv1.OrganizationSummary
	for _, o := range list.Msg.GetOrganizations() {
		switch o.GetId() {
		case orgAlpha:
			alpha = o
		case empty.Msg.GetOrganization().GetId():
			none = o
		}
	}
	if alpha == nil || alpha.GetNodeCount() < 1 || alpha.GetStorageBytes() <= 0 || alpha.GetMemberCount() < 1 {
		t.Errorf("Alpha = %v", alpha)
	}
	if none == nil || none.GetNodeCount() != 0 || none.GetStorageBytes() != 0 {
		t.Errorf("empty organization = %v", none)
	}
	if strings.Contains(list.Msg.String(), "alpha-01") || strings.Contains(list.Msg.String(), "ann@example.org") {
		t.Errorf("the list shows tenant data: %s", list.Msg.String())
	}
}

// At a quota something new is refused, naming the quota; what exists keeps running, and the
// admins see the state (M2-32).
func TestOrganizationAPI_QuotasRefuseNewNodesAndMembers(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	sam := superadmin(t, s)
	ann := login(t, s, "ann@example.org")
	ctx := context.Background()
	defer setQuotas(t, sam, orgAlpha, "Alpha", &webv1.OrganizationQuotas{})

	state, err := ann.access.GetOrganizationQuotas(ctx, connect.NewRequest(&webv1.GetOrganizationQuotasRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	nodes, members := state.Msg.GetNodes(), state.Msg.GetMembers()+state.Msg.GetOpenInvitations()
	if nodes < 1 || members < 1 || state.Msg.GetQuotas().MaxNodes != nil {
		t.Fatalf("state = %v", state.Msg)
	}

	// Room for one more member: one invitation passes, the next is refused.
	setQuotas(t, sam, orgAlpha, "Alpha", &webv1.OrganizationQuotas{MaxMembers: u32(uint32(members) + 1), MaxNodes: u32(uint32(nodes))})
	first := "quota-" + uuid.NewString()[:8] + "@example.org"
	if _, err := ann.access.InviteMember(ctx, connect.NewRequest(&webv1.InviteMemberRequest{Email: first, Role: webv1.Role_ROLE_VIEWER})); err != nil {
		t.Fatal(err)
	}
	_, err = ann.access.InviteMember(ctx, connect.NewRequest(&webv1.InviteMemberRequest{Email: "quota-" + uuid.NewString()[:8] + "@example.org", Role: webv1.Role_ROLE_VIEWER}))
	want := "quota of " + strconv.FormatInt(members+1, 10) + " members"
	if connect.CodeOf(err) != connect.CodeResourceExhausted || !strings.Contains(err.Error(), want) {
		t.Errorf("second invitation: %v, want RESOURCE_EXHAUSTED naming %q", err, want)
	}
	// Inviting the same address again replaces its invitation, it takes no more room.
	invited, err := ann.access.InviteMember(ctx, connect.NewRequest(&webv1.InviteMemberRequest{Email: first, Role: webv1.Role_ROLE_OPERATOR}))
	if err != nil || invited.Msg.GetToken() == "" {
		t.Fatalf("invite again: %v, %v", invited, err)
	}

	// A lowered quota refuses inviting and accepting, the invitation stays; nobody is removed.
	setQuotas(t, sam, orgAlpha, "Alpha", &webv1.OrganizationQuotas{MaxMembers: u32(1), MaxNodes: u32(uint32(nodes))})
	if _, err := ann.access.InviteMember(ctx, connect.NewRequest(&webv1.InviteMemberRequest{Email: first, Role: webv1.Role_ROLE_VIEWER})); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Errorf("invite under a lowered quota: %v", err)
	}
	_, err = anonymous(s).AcceptInvitation(ctx, connect.NewRequest(&webv1.AcceptInvitationRequest{Token: invited.Msg.GetToken(), DisplayName: "Quota", Password: userPassword}))
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Errorf("accept over the quota: %v, want RESOURCE_EXHAUSTED", err)
	}
	var open int
	ownerQueryRow(t, "SELECT count(*) FROM invitations WHERE email = '"+first+"'", &open)
	if open != 1 {
		t.Errorf("the invitation is gone after the refused acceptance (%d)", open)
	}
	after, err := ann.access.ListMembers(ctx, connect.NewRequest(&webv1.ListMembersRequest{}))
	if err != nil || int64(len(after.Msg.GetMembers())) != state.Msg.GetMembers() {
		t.Errorf("members after lowering the quota: %d, %v", len(after.Msg.GetMembers()), err)
	}

	// Nodes: at the quota a new enrollment is refused and the token keeps its uses.
	token2, err := ann.enrollment.CreateEnrollmentToken(ctx, connect.NewRequest(&webv1.CreateEnrollmentTokenRequest{GroupId: groupAl, MaxUses: 1}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = agentClient(s).Enroll(ctx, enrollRequest(token2.Msg.GetSecret(), p256CSR(t), newMachineID()))
	want = "quota of " + strconv.FormatInt(nodes, 10) + " nodes"
	if connect.CodeOf(err) != connect.CodeResourceExhausted || !strings.Contains(err.Error(), want) {
		t.Errorf("enroll at the quota: %v, want RESOURCE_EXHAUSTED naming %q", err, want)
	}
	state, err = ann.access.GetOrganizationQuotas(ctx, connect.NewRequest(&webv1.GetOrganizationQuotasRequest{}))
	if err != nil || state.Msg.GetQuotas().GetMaxNodes() != uint32(nodes) || state.Msg.GetNodes() != nodes {
		t.Errorf("state at the quota = %v, %v", state.Msg, err)
	}
	// Raised again, the same token enrolls.
	setQuotas(t, sam, orgAlpha, "Alpha", &webv1.OrganizationQuotas{MaxNodes: u32(uint32(nodes) + 1)})
	if _, err := agentClient(s).Enroll(ctx, enrollRequest(token2.Msg.GetSecret(), p256CSR(t), newMachineID())); err != nil {
		t.Errorf("enroll after raising the quota: %v", err)
	}
}
