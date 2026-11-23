package webapi_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

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
	node                            webv1connect.NodeServiceClient
	agentCA                         webv1connect.AgentCAServiceClient
	metrics                         webv1connect.MetricsServiceClient
	fleet                           webv1connect.FleetServiceClient
	events                          webv1connect.SecurityEventServiceClient
	channels                        webv1connect.NotificationChannelServiceClient
	policies                        webv1connect.NotificationPolicyServiceClient
	silences                        webv1connect.AlertSilenceServiceClient
	alerts                          webv1connect.AlertServiceClient
	rules                           webv1connect.AlertRuleServiceClient
	windows                         webv1connect.MaintenanceWindowServiceClient
	logSettings                     webv1connect.LogSettingsServiceClient
	orgA, groupA, memberA, bindingA string
	invitationA, inviteTokenA       string // an open invitation of Org A for another address
	enrollmentTokenA                string // an open enrollment token of Org A
	nodeA                           string // a node of Org A
	channelA                        string // a notification channel of Org A, with a delivery
	policyA                         string // a notification policy of Org A to channelA
	silenceA, windowA               string // a silence and a maintenance window of Org A's group
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

var errForeignAccepted = errors.New("a foreign ID was accepted")

func selectorA(e isolationEnv) *webv1.MetricSelector {
	return &webv1.MetricSelector{Metric: "sermon_load_average", NodeIds: []string{e.nodeA}}
}

func leaksChannel(e isolationEnv, s string) bool {
	return strings.Contains(s, e.channelA) || strings.Contains(s, "alpha-hook") || strings.Contains(s, "alpha.example") ||
		strings.Contains(s, "alpha-error") || strings.Contains(s, "alpha-01")
}

func policySpec(channel string) *webv1.PolicySpec {
	return &webv1.PolicySpec{Name: "pwned", Enabled: true, ChannelIds: []string{channel}, Grouping: webv1.PolicyGrouping_POLICY_GROUPING_NODE}
}

// alertKeyA is the brute-force alert of Org A's node.
func alertKeyA(e isolationEnv) *webv1.AlertKey {
	return &webv1.AlertKey{RuleId: "security.ssh.bruteforce", NodeId: e.nodeA, Dimension: "ip=192.0.2.7"}
}

func windowSpec(group string) *webv1.MaintenanceWindowSpec {
	s := &webv1.MaintenanceWindowSpec{
		Name: "pwned", Enabled: true, Days: []webv1.Weekday{webv1.Weekday_WEEKDAY_SUNDAY}, StartMinute: 0, DurationMinutes: 1440, TimeZone: "UTC",
	}
	if group != "" {
		s.GroupIds = []string{group}
	}
	return s
}

// tenantIsolationCases maps every tenant RPC to its cross-tenant case (isolation_integration_test.go).
var tenantIsolationCases = map[string]isolationCase{
	"/sermon.web.v1.NotificationChannelService/ListChannels": {list: true, run: func(ctx context.Context, e isolationEnv) error {
		res, err := e.channels.ListChannels(ctx, connect.NewRequest(&webv1.ListChannelsRequest{}))
		if err == nil && leaksChannel(e, res.Msg.String()) {
			return errLeak
		}
		return err
	}},
	"/sermon.web.v1.NotificationChannelService/CreateChannel": {list: true, run: func(ctx context.Context, e isolationEnv) error {
		// Creating acts in the caller's organization (Alpha's channels are checked afterwards).
		_, err := e.channels.CreateChannel(ctx, connect.NewRequest(&webv1.CreateChannelRequest{
			Type: "hook", Name: "created-by-b", Language: "en", Settings: map[string]string{"url": "https://beta.example/hook"},
			Secrets: map[string]string{"token": "beta-token"},
		}))
		return err
	}},
	"/sermon.web.v1.NotificationChannelService/UpdateChannel": {run: func(ctx context.Context, e isolationEnv) error {
		name := "pwned"
		_, err := e.channels.UpdateChannel(ctx, connect.NewRequest(&webv1.UpdateChannelRequest{
			ChannelId: e.channelA, Name: &name, ReplaceSettings: true, Settings: map[string]string{"url": "https://evil.example/"},
		}))
		return err
	}},
	"/sermon.web.v1.NotificationChannelService/DeleteChannel": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.channels.DeleteChannel(ctx, connect.NewRequest(&webv1.DeleteChannelRequest{ChannelId: e.channelA}))
		return err
	}},
	"/sermon.web.v1.NotificationChannelService/TestChannel": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.channels.TestChannel(ctx, connect.NewRequest(&webv1.TestChannelRequest{ChannelId: e.channelA}))
		return err
	}},
	"/sermon.web.v1.NotificationChannelService/ListDeliveries": {list: true, run: func(ctx context.Context, e isolationEnv) error {
		for _, channel := range []string{e.channelA, ""} {
			res, err := e.channels.ListDeliveries(ctx, connect.NewRequest(&webv1.ListDeliveriesRequest{ChannelId: channel}))
			if err != nil {
				return err
			}
			if leaksChannel(e, res.Msg.String()) {
				return errLeak
			}
		}
		return nil
	}},
	"/sermon.web.v1.NotificationPolicyService/ListPolicies": {list: true, run: func(ctx context.Context, e isolationEnv) error {
		res, err := e.policies.ListPolicies(ctx, connect.NewRequest(&webv1.ListPoliciesRequest{}))
		if err == nil && (strings.Contains(res.Msg.String(), e.policyA) || leaksChannel(e, res.Msg.String())) {
			return errLeak
		}
		return err
	}},
	"/sermon.web.v1.NotificationPolicyService/CreatePolicy": {list: true, run: func(ctx context.Context, e isolationEnv) error {
		// Creating acts in the caller's organization; a channel of Org A is unknown there.
		_, err := e.policies.CreatePolicy(ctx, connect.NewRequest(&webv1.CreatePolicyRequest{Spec: policySpec(e.channelA)}))
		if connect.CodeOf(err) == connect.CodeInvalidArgument {
			return nil
		}
		return errors.Join(errForeignAccepted, err)
	}},
	"/sermon.web.v1.NotificationPolicyService/UpdatePolicy": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.policies.UpdatePolicy(ctx, connect.NewRequest(&webv1.UpdatePolicyRequest{PolicyId: e.policyA, Spec: policySpec(e.channelA)}))
		return err
	}},
	"/sermon.web.v1.NotificationPolicyService/DeletePolicy": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.policies.DeletePolicy(ctx, connect.NewRequest(&webv1.DeletePolicyRequest{PolicyId: e.policyA}))
		return err
	}},
	// Rule IDs are the same in every organization: the calls act in the caller's own one, and Org A
	// keeps its switched-off swap rule (checked afterwards).
	"/sermon.web.v1.AlertRuleService/ListAlertRules": {list: true, run: func(ctx context.Context, e isolationEnv) error {
		res, err := e.rules.ListAlertRules(ctx, connect.NewRequest(&webv1.ListAlertRulesRequest{}))
		if err != nil {
			return err
		}
		for _, r := range res.Msg.GetRules() {
			if r.GetId() == "system.swap.usage_high" && !r.GetEnabled() {
				return errLeak
			}
		}
		return nil
	}},
	"/sermon.web.v1.AlertRuleService/ImportAlertRules": {list: true, run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.rules.ImportAlertRules(ctx, connect.NewRequest(&webv1.ImportAlertRulesRequest{Yaml: "apiVersion: sermon/v1\n", DryRun: true}))
		return err
	}},
	"/sermon.web.v1.AlertRuleService/SetAlertRuleEnabled": {list: true, run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.rules.SetAlertRuleEnabled(ctx, connect.NewRequest(&webv1.SetAlertRuleEnabledRequest{RuleId: "system.swap.usage_high", Enabled: true}))
		return err
	}},
	"/sermon.web.v1.AlertRuleService/ResetAlertRule": {list: true, run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.rules.ResetAlertRule(ctx, connect.NewRequest(&webv1.ResetAlertRuleRequest{RuleId: "system.swap.usage_high"}))
		if connect.CodeOf(err) == connect.CodeNotFound {
			return nil // Org B has nothing to reset
		}
		return err
	}},
	"/sermon.web.v1.AlertRuleService/ExportAlertRules": {list: true, run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.rules.ExportAlertRules(ctx, connect.NewRequest(&webv1.ExportAlertRulesRequest{}))
		return err
	}},
	"/sermon.web.v1.AlertService/ListAlerts": {list: true, run: func(ctx context.Context, e isolationEnv) error {
		res, err := e.alerts.ListAlerts(ctx, connect.NewRequest(&webv1.ListAlertsRequest{}))
		if err == nil && (strings.Contains(res.Msg.String(), e.nodeA) || strings.Contains(res.Msg.String(), "alpha-user") || leaks(e, res.Msg.String())) {
			return errLeak
		}
		return err
	}},
	"/sermon.web.v1.AlertService/GetAlertHistory": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.alerts.GetAlertHistory(ctx, connect.NewRequest(&webv1.GetAlertHistoryRequest{Key: alertKeyA(e)}))
		return err
	}},
	"/sermon.web.v1.AlertService/AcknowledgeAlert": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.alerts.AcknowledgeAlert(ctx, connect.NewRequest(&webv1.AcknowledgeAlertRequest{Key: alertKeyA(e)}))
		return err
	}},
	"/sermon.web.v1.AlertSilenceService/ListSilences": {list: true, run: func(ctx context.Context, e isolationEnv) error {
		res, err := e.silences.ListSilences(ctx, connect.NewRequest(&webv1.ListSilencesRequest{}))
		if err == nil && (strings.Contains(res.Msg.String(), e.silenceA) || strings.Contains(res.Msg.String(), "alpha-silence") || leaks(e, res.Msg.String())) {
			return errLeak
		}
		return err
	}},
	"/sermon.web.v1.AlertSilenceService/CreateSilence": {list: true, run: func(ctx context.Context, e isolationEnv) error {
		// Creating acts in the caller's organization; a group of Org A is unknown there.
		_, err := e.silences.CreateSilence(ctx, connect.NewRequest(&webv1.CreateSilenceRequest{Spec: &webv1.SilenceSpec{
			Matchers: &webv1.PolicyMatchers{GroupIds: []string{e.groupA}}, NodeIds: []string{e.nodeA},
			EndTime: timestamppb.New(time.Now().Add(time.Hour)), Comment: "pwned",
		}}))
		if connect.CodeOf(err) == connect.CodeInvalidArgument {
			return nil
		}
		return errors.Join(errForeignAccepted, err)
	}},
	"/sermon.web.v1.AlertSilenceService/ExpireSilence": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.silences.ExpireSilence(ctx, connect.NewRequest(&webv1.ExpireSilenceRequest{SilenceId: e.silenceA}))
		return err
	}},
	"/sermon.web.v1.MaintenanceWindowService/ListMaintenanceWindows": {list: true, run: func(ctx context.Context, e isolationEnv) error {
		res, err := e.windows.ListMaintenanceWindows(ctx, connect.NewRequest(&webv1.ListMaintenanceWindowsRequest{}))
		if err == nil && (strings.Contains(res.Msg.String(), e.windowA) || strings.Contains(res.Msg.String(), "alpha-window") || leaks(e, res.Msg.String())) {
			return errLeak
		}
		return err
	}},
	"/sermon.web.v1.MaintenanceWindowService/CreateMaintenanceWindow": {list: true, run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.windows.CreateMaintenanceWindow(ctx, connect.NewRequest(&webv1.CreateMaintenanceWindowRequest{Spec: windowSpec(e.groupA)}))
		if connect.CodeOf(err) == connect.CodeInvalidArgument {
			return nil
		}
		return errors.Join(errForeignAccepted, err)
	}},
	"/sermon.web.v1.MaintenanceWindowService/UpdateMaintenanceWindow": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.windows.UpdateMaintenanceWindow(ctx, connect.NewRequest(&webv1.UpdateMaintenanceWindowRequest{WindowId: e.windowA, Spec: windowSpec("")}))
		return err
	}},
	"/sermon.web.v1.MaintenanceWindowService/DeleteMaintenanceWindow": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.windows.DeleteMaintenanceWindow(ctx, connect.NewRequest(&webv1.DeleteMaintenanceWindowRequest{WindowId: e.windowA}))
		return err
	}},
	"/sermon.web.v1.SecurityEventService/ListSecurityEvents": {list: true, run: func(ctx context.Context, e isolationEnv) error {
		res, err := e.events.ListSecurityEvents(ctx, connect.NewRequest(&webv1.ListSecurityEventsRequest{
			From: timestamppb.New(time.Now().Add(-24 * time.Hour)), To: timestamppb.New(time.Now().Add(time.Hour)), NodeIds: []string{e.nodeA},
		}))
		if err != nil {
			return err
		}
		if len(res.Msg.GetEvents()) > 0 || strings.Contains(res.Msg.String(), e.nodeA) || strings.Contains(res.Msg.String(), "alpha-user") {
			return errLeak
		}
		return nil
	}},
	"/sermon.web.v1.MetricsService/QueryMetrics": {run: func(ctx context.Context, e isolationEnv) error {
		now := time.Now()
		_, err := e.metrics.QueryMetrics(ctx, connect.NewRequest(&webv1.QueryMetricsRequest{
			Selector: selectorA(e), From: timestamppb.New(now.Add(-time.Hour)), To: timestamppb.New(now),
		}))
		return err
	}},
	"/sermon.web.v1.MetricsService/WatchMetrics": {run: func(ctx context.Context, e isolationEnv) error {
		stream, err := e.metrics.WatchMetrics(ctx, connect.NewRequest(&webv1.WatchMetricsRequest{Selector: selectorA(e)}))
		if err != nil {
			return err
		}
		defer func() { _ = stream.Close() }()
		if stream.Receive() {
			return errLeak
		}
		return stream.Err()
	}},
	"/sermon.web.v1.FleetService/GetFleetSummary": {list: true, run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.fleet.GetFleetSummary(ctx, connect.NewRequest(&webv1.GetFleetSummaryRequest{GroupId: e.groupA}))
		if connect.CodeOf(err) != connect.CodeNotFound {
			return fmt.Errorf("%w: group filter: %w", errForeignAccepted, err)
		}
		res, err := e.fleet.GetFleetSummary(ctx, connect.NewRequest(&webv1.GetFleetSummaryRequest{}))
		if err == nil && (leaks(e, res.Msg.String()) || strings.Contains(res.Msg.String(), e.nodeA)) {
			return errLeak
		}
		return err
	}},
	"/sermon.web.v1.NodeService/GetNode": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.node.GetNode(ctx, connect.NewRequest(&webv1.GetNodeRequest{NodeId: e.nodeA}))
		return err
	}},
	"/sermon.web.v1.NodeService/ListFilesystems": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.node.ListFilesystems(ctx, connect.NewRequest(&webv1.ListFilesystemsRequest{NodeId: e.nodeA}))
		return err
	}},
	"/sermon.web.v1.AuthService/SwitchOrganization": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.auth.SwitchOrganization(ctx, connect.NewRequest(&webv1.SwitchOrganizationRequest{OrganizationId: e.orgA}))
		return err
	}},
	"/sermon.web.v1.LogSettingsService/SetGroupLogSettings": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.logSettings.SetGroupLogSettings(ctx, connect.NewRequest(&webv1.SetGroupLogSettingsRequest{GroupId: e.groupA, Settings: &webv1.LogSettings{}}))
		return err
	}},
	"/sermon.web.v1.LogSettingsService/ResetGroupLogSettings": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.logSettings.ResetGroupLogSettings(ctx, connect.NewRequest(&webv1.ResetGroupLogSettingsRequest{GroupId: e.groupA}))
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
	"/sermon.web.v1.AgentCAService/GetAgentCAStatus": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.agentCA.GetAgentCAStatus(ctx, connect.NewRequest(&webv1.GetAgentCAStatusRequest{}))
		return err
	}},
	"/sermon.web.v1.AgentCAService/StartAgentCARollover": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.agentCA.StartAgentCARollover(ctx, connect.NewRequest(&webv1.StartAgentCARolloverRequest{}))
		return err
	}},
	"/sermon.web.v1.AgentCAService/RetireAgentCA": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.agentCA.RetireAgentCA(ctx, connect.NewRequest(&webv1.RetireAgentCARequest{Id: 1}))
		return err
	}},
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
	// M2-32: only superadmins change organizations; an org admin of B gets PERMISSION_DENIED.
	"/sermon.web.v1.OrganizationService/UpdateOrganization": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.organization.UpdateOrganization(ctx, connect.NewRequest(&webv1.UpdateOrganizationRequest{
			OrganizationId: e.orgA, Name: "pwned", Quotas: &webv1.OrganizationQuotas{},
		}))
		return err
	}},
	// M2-33: only superadmins lock and delete organizations; an org admin of B gets PERMISSION_DENIED.
	"/sermon.web.v1.OrganizationService/LockOrganization": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.organization.LockOrganization(ctx, connect.NewRequest(&webv1.LockOrganizationRequest{OrganizationId: e.orgA, ConfirmName: "Alpha"}))
		return err
	}},
	"/sermon.web.v1.OrganizationService/ScheduleOrganizationDeletion": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.organization.ScheduleOrganizationDeletion(ctx, connect.NewRequest(&webv1.ScheduleOrganizationDeletionRequest{OrganizationId: e.orgA, ConfirmName: "Alpha"}))
		return err
	}},
	"/sermon.web.v1.OrganizationService/UnlockOrganization": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.organization.UnlockOrganization(ctx, connect.NewRequest(&webv1.UnlockOrganizationRequest{OrganizationId: e.orgA, ConfirmName: "Alpha"}))
		return err
	}},
	"/sermon.web.v1.OrganizationService/DeleteOrganization": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.organization.DeleteOrganization(ctx, connect.NewRequest(&webv1.DeleteOrganizationRequest{OrganizationId: e.orgA, ConfirmName: "Alpha"}))
		return err
	}},
	"/sermon.web.v1.OrganizationService/ExportOrganizationAudit": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.organization.ExportOrganizationAudit(ctx, connect.NewRequest(&webv1.ExportOrganizationAuditRequest{OrganizationId: e.orgA}))
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
	"/sermon.web.v1.NodeService/RotateNodeKeys": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.node.RotateNodeKeys(ctx, connect.NewRequest(&webv1.RotateNodeKeysRequest{
			Target: &webv1.RotateNodeKeysRequest_Nodes{Nodes: &webv1.NodeIDs{Ids: []string{e.nodeA}}},
		}))
		return err
	}},
	// M2-23: a node of another organization is NOT_FOUND, its lock and window untouched.
	"/sermon.web.v1.NodeService/UnlockNode": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.node.UnlockNode(ctx, connect.NewRequest(&webv1.UnlockNodeRequest{NodeId: e.nodeA}))
		return err
	}},
	"/sermon.web.v1.NodeService/AllowNodeTakeover": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.node.AllowNodeTakeover(ctx, connect.NewRequest(&webv1.AllowNodeTakeoverRequest{NodeId: e.nodeA}))
		return err
	}},
	// M2-29: tags of a node of another organization are NOT_FOUND and stay as they are.
	"/sermon.web.v1.NodeService/SetNodeTags": {run: func(ctx context.Context, e isolationEnv) error {
		_, err := e.node.SetNodeTags(ctx, connect.NewRequest(&webv1.SetNodeTagsRequest{NodeId: e.nodeA, Tags: map[string]string{"env": "prod"}}))
		return err
	}},
	// M2-32: the quotas and counts of the caller's own organization only.
	"/sermon.web.v1.AccessService/GetOrganizationQuotas": {list: true, run: func(ctx context.Context, e isolationEnv) error {
		res, err := e.access.GetOrganizationQuotas(ctx, connect.NewRequest(&webv1.GetOrganizationQuotasRequest{}))
		if err == nil && leaks(e, res.Msg.String()) {
			return errLeak
		}
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
