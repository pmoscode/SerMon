//go:build integration

package webapi_test

import (
	"context"
	"errors"
	"net/http/cookiejar"
	"sort"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/loki"
	"github.com/pmoscode/SerMon/server/internal/adapters/loki/lokitest"
	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/pgtest"
	"github.com/pmoscode/SerMon/server/internal/app/ingest"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
	"github.com/pmoscode/SerMon/server/internal/transport/webapi"
)

// Fixtures of the access tests (Org "Alpha" and Org "Beta").
const (
	orgAlpha  = "0193f5a0-0000-7000-8000-0000000001aa"
	orgBeta   = "0193f5a0-0000-7000-8000-0000000001bb"
	annID     = "0193f5a0-0000-7000-8000-0000000001a1" // admin of Alpha
	miaID     = "0193f5a0-0000-7000-8000-0000000001a2" // member of Alpha (viewer on groupAlpha)
	beaID     = "0193f5a0-0000-7000-8000-0000000001b1" // admin of Beta
	kaiID     = "0193f5a0-0000-7000-8000-0000000001c1" // member of both, no roles in Beta
	groupAl   = "0193f5a0-0000-7000-8000-0000000001a9"
	bindingAl = "0193f5a0-0000-7000-8000-0000000001a8"
	inviteAl  = "0193f5a0-0000-7000-8000-0000000001a7"
	tokenAl   = "0193f5a0-0000-7000-8000-0000000001a6" // enrollment token of Alpha
	nodeAl    = "0193f5a0-0000-7000-8000-0000000001a5" // node of Alpha (offline)
	certAl    = "7a0000000000000000000000000001a5"     // its certificate serial
	// inviteTokenAl is the link token of inviteAl (fixture, not a secret).
	inviteTokenAl = "alphaInvitationTokenForCrossTenantTests0001" // betterleaks:allow
)

func seedAccessFixtures(t *testing.T) {
	t.Helper()
	ownerSQL(t,
		"INSERT INTO organizations (id, name) VALUES ('"+orgAlpha+"', 'Alpha'), ('"+orgBeta+"', 'Beta') ON CONFLICT DO NOTHING")
	seedLogin(t, annID, "ann@example.org", orgAlpha)
	seedLogin(t, miaID, "mia@example.org", orgAlpha)
	seedLogin(t, beaID, "bea@example.org", orgBeta)
	seedLogin(t, kaiID, "kai@example.org", orgAlpha, orgBeta)
	ownerSQL(t,
		"DELETE FROM user_totp WHERE user_id IN ('"+annID+"', '"+miaID+"', '"+beaID+"', '"+kaiID+"')",
		"UPDATE organizations SET require_second_factor = false WHERE id IN ('"+orgAlpha+"', '"+orgBeta+"')")
	ownerSQL(t,
		"SELECT set_config('sermon.org_id', '"+orgAlpha+"', false)",
		"INSERT INTO node_groups (id, org_id, name) VALUES ('"+groupAl+"', '"+orgAlpha+"', 'alpha-web') ON CONFLICT DO NOTHING",
		"DELETE FROM role_bindings WHERE org_id = '"+orgAlpha+"'",
		"INSERT INTO role_bindings (id, org_id, user_id, role) VALUES (gen_random_uuid(), '"+orgAlpha+"', '"+annID+"', 'admin')",
		"INSERT INTO role_bindings (id, org_id, user_id, group_id, role) VALUES ('"+bindingAl+"', '"+orgAlpha+"', '"+miaID+"', '"+groupAl+"', 'viewer')",
		"INSERT INTO role_bindings (id, org_id, user_id, role) VALUES (gen_random_uuid(), '"+orgAlpha+"', '"+kaiID+"', 'viewer')",
		"INSERT INTO audit_log (org_id, seq, action, target, details, prev_hash, hash) VALUES ('"+orgAlpha+"', 0, 'seed', 'by-a', '{}', '\\x', '\\x')",
		"DELETE FROM invitations WHERE org_id = '"+orgAlpha+"'",
		"DELETE FROM enrollment_tokens WHERE org_id = '"+orgAlpha+"'",
		"INSERT INTO nodes (id, org_id, group_id, hostname, machine_id, os_id, os_version, kernel_release, architecture, cpu_count, memory_bytes) VALUES ('"+nodeAl+"', '"+orgAlpha+"', '"+groupAl+"', 'alpha-01', '"+strings.Repeat("a", 32)+"', 'debian', '13', '6.12.0', 'amd64', 2, 1073741824) ON CONFLICT DO NOTHING",
		"DELETE FROM agent_certificates WHERE node_id = '"+nodeAl+"'",
		"INSERT INTO agent_certificates (serial, org_id, node_id, authority_id, not_before, not_after) VALUES ('"+certAl+"', '"+orgAlpha+"', '"+nodeAl+"', (SELECT id FROM pki_authorities WHERE active), now() - interval '1 hour', now() + interval '30 days')",
		"INSERT INTO enrollment_tokens (id, org_id, group_id, token_hash, expires_at, max_uses) VALUES ('"+tokenAl+"', '"+orgAlpha+"', '"+groupAl+"', sha256(convert_to('"+tokenAl+"', 'UTF8')), now() + interval '1 hour', 1)",
		"INSERT INTO invitations (id, org_id, email, role, token_hash, expires_at) VALUES ('"+inviteAl+"', '"+orgAlpha+"', 'invited-a@example.org', 'admin', sha256(convert_to('"+inviteTokenAl+"', 'UTF8')), now() + interval '1 day')",
		"INSERT INTO security_events (org_id, node_id, occurred_at, kind, user_name, remote_ip, unverified) SELECT '"+orgAlpha+"', '"+nodeAl+"', now() - interval '1 minute', 'ssh.login.failed', 'alpha-user', '192.0.2.77', true WHERE NOT EXISTS (SELECT 1 FROM security_events WHERE user_name = 'alpha-user')",
		"SELECT set_config('sermon.org_id', '"+orgBeta+"', false)",
		"DELETE FROM role_bindings WHERE org_id = '"+orgBeta+"'",
		"INSERT INTO role_bindings (id, org_id, user_id, role) VALUES (gen_random_uuid(), '"+orgBeta+"', '"+beaID+"', 'admin')",
	)
}

type loggedIn struct {
	auth         webv1connect.AuthServiceClient
	access       webv1connect.AccessServiceClient
	system       webv1connect.SystemServiceClient
	invitation   webv1connect.InvitationServiceClient
	organization webv1connect.OrganizationServiceClient
	enrollment   webv1connect.EnrollmentTokenServiceClient
	node         webv1connect.NodeServiceClient
	agentCA      webv1connect.AgentCAServiceClient
	metrics      webv1connect.MetricsServiceClient
	fleet        webv1connect.FleetServiceClient
	twoFactor    webv1connect.TwoFactorServiceClient
	events       webv1connect.SecurityEventServiceClient
	channels     webv1connect.NotificationChannelServiceClient
	policies     webv1connect.NotificationPolicyServiceClient
	silences     webv1connect.AlertSilenceServiceClient
	alerts       webv1connect.AlertServiceClient
	rules        webv1connect.AlertRuleServiceClient
	windows      webv1connect.MaintenanceWindowServiceClient
	policy       webv1connect.SecurityPolicyServiceClient
	logSettings  webv1connect.LogSettingsServiceClient
	logs         webv1connect.LogServiceClient
	liveView     webv1connect.LiveViewServiceClient
	jar          *cookiejar.Jar
}

func login(t *testing.T, s stack, email string) loggedIn {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	hc := s.httpClient(jar, s.origin)
	l := loggedIn{
		auth:         webv1connect.NewAuthServiceClient(hc, s.server.URL),
		access:       webv1connect.NewAccessServiceClient(hc, s.server.URL),
		system:       webv1connect.NewSystemServiceClient(hc, s.server.URL),
		invitation:   webv1connect.NewInvitationServiceClient(hc, s.server.URL),
		organization: webv1connect.NewOrganizationServiceClient(hc, s.server.URL),
		enrollment:   webv1connect.NewEnrollmentTokenServiceClient(hc, s.server.URL),
		node:         webv1connect.NewNodeServiceClient(hc, s.server.URL),
		agentCA:      webv1connect.NewAgentCAServiceClient(hc, s.server.URL),
		metrics:      webv1connect.NewMetricsServiceClient(hc, s.server.URL),
		fleet:        webv1connect.NewFleetServiceClient(hc, s.server.URL),
		twoFactor:    webv1connect.NewTwoFactorServiceClient(hc, s.server.URL),
		events:       webv1connect.NewSecurityEventServiceClient(hc, s.server.URL),
		channels:     webv1connect.NewNotificationChannelServiceClient(hc, s.server.URL),
		policies:     webv1connect.NewNotificationPolicyServiceClient(hc, s.server.URL),
		silences:     webv1connect.NewAlertSilenceServiceClient(hc, s.server.URL),
		alerts:       webv1connect.NewAlertServiceClient(hc, s.server.URL),
		rules:        webv1connect.NewAlertRuleServiceClient(hc, s.server.URL),
		windows:      webv1connect.NewMaintenanceWindowServiceClient(hc, s.server.URL),
		policy:       webv1connect.NewSecurityPolicyServiceClient(hc, s.server.URL),
		logSettings:  webv1connect.NewLogSettingsServiceClient(hc, s.server.URL),
		logs:         webv1connect.NewLogServiceClient(hc, s.server.URL),
		liveView:     webv1connect.NewLiveViewServiceClient(hc, s.server.URL),
		jar:          jar,
	}
	if _, err := l.auth.Login(context.Background(), connect.NewRequest(&webv1.LoginRequest{Email: email, Password: userPassword})); err != nil {
		t.Fatalf("login %s: %v", email, err)
	}
	return l
}

func TestTenantIsolation_AdminOfAnotherOrganizationCannotTouchForeignData(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	seedChannelAlpha(t)
	seedLogAlpha(t)
	bea := login(t, s, "bea@example.org")
	env := isolationEnv{
		auth: bea.auth, access: bea.access, invitation: bea.invitation, organization: bea.organization, enrollment: bea.enrollment, node: bea.node, agentCA: bea.agentCA,
		metrics: bea.metrics, fleet: bea.fleet, events: bea.events, channels: bea.channels, channelA: channelAl, policies: bea.policies, policyA: policyAl,
		silences: bea.silences, windows: bea.windows, alerts: bea.alerts, rules: bea.rules, silenceA: silenceAl, windowA: windowAl,
		logSettings: bea.logSettings, logs: bea.logs, liveView: bea.liveView,
		orgA: orgAlpha, groupA: groupAl, memberA: miaID, bindingA: bindingAl, invitationA: inviteAl, inviteTokenA: inviteTokenAl,
		enrollmentTokenA: tokenAl, nodeA: nodeAl,
	}
	ctx := context.Background()

	procedures := make([]string, 0, len(tenantIsolationCases))
	for p := range tenantIsolationCases {
		procedures = append(procedures, p)
	}
	sort.Strings(procedures)
	for _, p := range procedures {
		c := tenantIsolationCases[p]
		err := c.run(ctx, env)
		code := connect.CodeOf(err)
		switch {
		case errors.Is(err, errLeak):
			t.Errorf("%s leaks data of another organization", p)
		case c.list && err != nil:
			t.Errorf("%s: %v", p, err)
		case c.tokenBound && code == connect.CodeUnauthenticated:
		case !c.list && code != connect.CodeNotFound && code != connect.CodePermissionDenied:
			t.Errorf("%s with foreign IDs: %v, want NOT_FOUND or PERMISSION_DENIED", p, err)
		}
	}

	// Org Alpha is unchanged.
	conn, err := pgx.Connect(ctx, pgtest.Shared(t).OwnerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, "SELECT set_config('sermon.org_id', $1, false)", orgAlpha); err != nil {
		t.Fatal(err)
	}
	var name string
	var bindings, members, foreignGroups, invitations int
	var orgName string
	if err := conn.QueryRow(ctx, "SELECT name FROM organizations WHERE id = $1", orgAlpha).Scan(&orgName); err != nil || orgName != "Alpha" {
		t.Errorf("name of Alpha = %q, %v", orgName, err)
	}
	if err := conn.QueryRow(ctx, "SELECT name FROM node_groups WHERE id = $1", groupAl).Scan(&name); err != nil || name != "alpha-web" {
		t.Errorf("group of Alpha = %q, %v", name, err)
	}
	_ = conn.QueryRow(ctx, "SELECT count(*) FROM role_bindings WHERE id = $1", bindingAl).Scan(&bindings)
	_ = conn.QueryRow(ctx, "SELECT count(*) FROM memberships WHERE user_id = $1", miaID).Scan(&members)
	// The owner is a superuser in the test container (RLS does not apply): filter explicitly.
	_ = conn.QueryRow(ctx, "SELECT count(*) FROM node_groups WHERE org_id = $1 AND name LIKE 'created-by-b%'", orgAlpha).Scan(&foreignGroups)
	_ = conn.QueryRow(ctx, "SELECT count(*) FROM invitations WHERE id = $1", inviteAl).Scan(&invitations)
	var revokedTokens int
	_ = conn.QueryRow(ctx, "SELECT count(*) FROM enrollment_tokens WHERE org_id = $1 AND (revoked_at IS NOT NULL OR group_id <> $2 OR id <> $3)", orgAlpha, groupAl, tokenAl).Scan(&revokedTokens)
	var revokedCerts int
	_ = conn.QueryRow(ctx, "SELECT count(*) FROM agent_certificates WHERE serial = $1 AND revoked_at IS NOT NULL", certAl).Scan(&revokedCerts)
	if revokedCerts != 0 {
		t.Error("the certificate of Alpha's node was revoked by Beta")
	}
	if revokedTokens != 0 {
		t.Errorf("enrollment tokens of Alpha changed or added: %d", revokedTokens)
	}
	var alphaChannel string
	var alphaChannels, alphaDeliveries int
	_ = conn.QueryRow(ctx, "SELECT name FROM notification_channels WHERE id = $1", channelAl).Scan(&alphaChannel)
	_ = conn.QueryRow(ctx, "SELECT count(*) FROM notification_channels WHERE org_id = $1", orgAlpha).Scan(&alphaChannels)
	_ = conn.QueryRow(ctx, "SELECT count(*) FROM notification_deliveries WHERE org_id = $1", orgAlpha).Scan(&alphaDeliveries)
	var alphaPolicy string
	var alphaPolicyChannels int
	_ = conn.QueryRow(ctx, "SELECT name FROM notification_policies WHERE id = $1", policyAl).Scan(&alphaPolicy)
	_ = conn.QueryRow(ctx, "SELECT count(*) FROM notification_policy_channels WHERE org_id = $1", orgAlpha).Scan(&alphaPolicyChannels)
	var alphaWindow string
	var alphaSilenceOpen bool
	_ = conn.QueryRow(ctx, "SELECT name FROM maintenance_windows WHERE id = $1", windowAl).Scan(&alphaWindow)
	_ = conn.QueryRow(ctx, "SELECT ends_at > now() AND expired_by IS NULL FROM alert_silences WHERE id = $1", silenceAl).Scan(&alphaSilenceOpen)
	var alphaSwapOff bool
	_ = conn.QueryRow(ctx, "SELECT disabled FROM alert_rule_overrides WHERE org_id = $1 AND rule_id = 'system.swap.usage_high'", orgAlpha).Scan(&alphaSwapOff)
	if !alphaSwapOff {
		t.Error("Alpha's switched-off rule changed")
	}
	var alphaLogSettings int
	_ = conn.QueryRow(ctx, "SELECT count(*) FROM log_settings WHERE org_id = $1", orgAlpha).Scan(&alphaLogSettings)
	if alphaLogSettings != 0 {
		t.Errorf("Beta wrote %d log settings for Alpha's groups", alphaLogSettings)
	}
	var alphaAcked int
	_ = conn.QueryRow(ctx, "SELECT count(*) FROM alert_instances WHERE org_id = $1 AND ack_by IS NOT NULL", orgAlpha).Scan(&alphaAcked)
	if alphaAcked != 0 {
		t.Error("Beta acknowledged Alpha's alert")
	}
	if alphaWindow != "alpha-window" || !alphaSilenceOpen {
		t.Errorf("Alpha's window %q or silence (open %v) changed", alphaWindow, alphaSilenceOpen)
	}
	if alphaPolicy != "alpha-policy" || alphaPolicyChannels != 1 {
		t.Errorf("Alpha's policy changed: %q, %d channels", alphaPolicy, alphaPolicyChannels)
	}
	if alphaChannel != "alpha-hook" || alphaChannels != 1 || alphaDeliveries != 1 {
		t.Errorf("Alpha's channels changed: %q, %d channels, %d deliveries", alphaChannel, alphaChannels, alphaDeliveries)
	}
	var beaInAlpha int
	_ = conn.QueryRow(ctx, "SELECT count(*) FROM memberships WHERE org_id = $1 AND user_id = $2", orgAlpha, beaID).Scan(&beaInAlpha)
	if bindings != 1 || members != 1 || foreignGroups != 0 || invitations != 1 || beaInAlpha != 0 {
		t.Errorf("Alpha changed: binding %d, membership %d, groups created by Beta %d, invitation %d, Beta admin joined %d",
			bindings, members, foreignGroups, invitations, beaInAlpha)
	}
}

func TestAccessAPI_AdminManagesGroupsAndRolesWithAudit(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ann := login(t, s, "ann@example.org")
	ctx := context.Background()
	name := "db-" + uuid.NewString()[:8]

	created, err := ann.access.CreateNodeGroup(ctx, connect.NewRequest(&webv1.CreateNodeGroupRequest{Name: name}))
	if err != nil {
		t.Fatal(err)
	}
	group := created.Msg.GetGroup().GetId()
	if _, err := ann.access.CreateNodeGroup(ctx, connect.NewRequest(&webv1.CreateNodeGroupRequest{Name: name})); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Errorf("duplicate: %v, want ALREADY_EXISTS", err)
	}
	granted, err := ann.access.GrantRole(ctx, connect.NewRequest(&webv1.GrantRoleRequest{UserId: miaID, GroupId: group, Role: webv1.Role_ROLE_OPERATOR}))
	if err != nil {
		t.Fatal(err)
	}
	members, err := ann.access.ListMembers(ctx, connect.NewRequest(&webv1.ListMembersRequest{}))
	if err != nil || !strings.Contains(members.Msg.String(), granted.Msg.GetBindingId()) {
		t.Errorf("ListMembers() lacks the new binding: %v", err)
	}
	if _, err := ann.access.RevokeRole(ctx, connect.NewRequest(&webv1.RevokeRoleRequest{BindingId: granted.Msg.GetBindingId()})); err != nil {
		t.Fatal(err)
	}
	if _, err := ann.access.DeleteNodeGroup(ctx, connect.NewRequest(&webv1.DeleteNodeGroupRequest{GroupId: group})); err != nil {
		t.Fatal(err)
	}

	audit, err := ann.access.ListAuditLog(ctx, connect.NewRequest(&webv1.ListAuditLogRequest{PageSize: 4}))
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, e := range audit.Msg.GetEntries() {
		actions = append(actions, e.GetAction())
		if e.GetActorId() != annID {
			t.Errorf("entry %d actor = %q, want ann", e.GetSeq(), e.GetActorId())
		}
	}
	if got, want := strings.Join(actions, ","), "group.delete,role.revoke,role.grant,group.create"; got != want {
		t.Errorf("audit = %s, want %s", got, want)
	}

	// The last org admin cannot remove themselves.
	if _, err := ann.access.RemoveMember(ctx, connect.NewRequest(&webv1.RemoveMemberRequest{UserId: annID})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("remove last admin: %v, want FAILED_PRECONDITION", err)
	}
}

func TestAccessAPI_RoleChangesNeedARecentLogin(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ann := login(t, s, "ann@example.org")
	ownerSQL(t, "UPDATE sessions SET step_up_at = now() - interval '1 hour' WHERE user_id = '"+annID+"'")

	_, err := ann.access.GrantRole(context.Background(), connect.NewRequest(&webv1.GrantRoleRequest{UserId: miaID, Role: webv1.Role_ROLE_VIEWER}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("GrantRole with an old login: %v, want FAILED_PRECONDITION", err)
	}
}

func TestAccessAPI_ViewersCannotManage(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	mia := login(t, s, "mia@example.org")
	ctx := context.Background()

	if _, err := mia.access.CreateNodeGroup(ctx, connect.NewRequest(&webv1.CreateNodeGroupRequest{Name: "x"})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("CreateNodeGroup by a viewer: %v, want PERMISSION_DENIED", err)
	}
	groups, err := mia.access.ListNodeGroups(ctx, connect.NewRequest(&webv1.ListNodeGroupsRequest{}))
	if err != nil || len(groups.Msg.GetGroups()) != 1 || groups.Msg.GetGroups()[0].GetId() != groupAl {
		t.Errorf("ListNodeGroups by a group viewer = %v, %v; want only her group", groups, err)
	}
}

func TestAccessAPI_SwitchOrganizationChangesTheActiveOrganization(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	kai := login(t, s, "kai@example.org")
	ctx := context.Background()

	res, err := kai.auth.SwitchOrganization(ctx, connect.NewRequest(&webv1.SwitchOrganizationRequest{OrganizationId: orgBeta}))
	if err != nil || res.Msg.GetSession().GetActiveOrganizationId() != orgBeta {
		t.Fatalf("SwitchOrganization(Beta) = %v, %v", res, err)
	}
	if !strings.Contains(res.Header().Get("Set-Cookie"), webapi.SessionCookie+"=") {
		t.Error("switching must issue a new session cookie")
	}
	// In Beta kai has no role: no groups visible, management denied.
	if groups, err := kai.access.ListNodeGroups(ctx, connect.NewRequest(&webv1.ListNodeGroupsRequest{})); err != nil || len(groups.Msg.GetGroups()) != 0 {
		t.Errorf("groups in Beta = %v, %v", groups, err)
	}
	back, err := kai.auth.SwitchOrganization(ctx, connect.NewRequest(&webv1.SwitchOrganizationRequest{OrganizationId: orgAlpha}))
	if err != nil || back.Msg.GetSession().GetActiveOrganizationId() != orgAlpha {
		t.Errorf("SwitchOrganization(Alpha) = %v, %v", back, err)
	}
	if _, err := kai.auth.SwitchOrganization(ctx, connect.NewRequest(&webv1.SwitchOrganizationRequest{OrganizationId: uuid.NewString()})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("switch to an unknown organization: %v, want NOT_FOUND", err)
	}
}

// seedLogAlpha writes a log line of Alpha's node to Loki (M3-08): a leaking query would find it.
func seedLogAlpha(t *testing.T) {
	t.Helper()
	writeLogAlpha(t, ingest.LogEntry{At: time.Now().Add(-time.Minute), Source: ingest.LogSourceJournal, Unit: "nginx.service", Severity: 4, Message: "alpha secret line"})
}

// writeLogAlpha writes entries of Alpha's node to Loki.
func writeLogAlpha(t *testing.T, entries ...ingest.LogEntry) {
	t.Helper()
	w := loki.NewWriter(loki.New(lokitest.Shared(t)))
	if err := w.Write(context.Background(), tenant.OrgID(uuid.MustParse(orgAlpha)), identity.NodeID(uuid.MustParse(nodeAl)), entries); err != nil {
		t.Fatal(err)
	}
}
