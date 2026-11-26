package webapi_test

import (
	"context"

	"connectrpc.com/connect"

	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// The step-up of sensitive actions (KONZEPT §7, M3-25). Every web API procedure that changes
// something is either in stepUpRequired – a call without a fresh step-up must fail with
// StepUpRequired – or in stepUpExempt with the reason it needs none. TestStepUp_EveryProcedureIsListed
// enforces this for new RPCs; the integration test calls every required one with an aged step-up:
// with the call of its tenant-isolation case on the caller's own objects, or with a case below.
var stepUpRequired = map[string]string{
	"/sermon.web.v1.AccessService/DeleteNodeGroup":                    "rights: takes the roles on the group",
	"/sermon.web.v1.AccessService/GrantRole":                          "rights",
	"/sermon.web.v1.AccessService/RevokeRole":                         "rights",
	"/sermon.web.v1.AccessService/RemoveMember":                       "rights",
	"/sermon.web.v1.AccessService/InviteMember":                       "rights: lets someone join with a role",
	"/sermon.web.v1.AgentCAService/StartAgentCARollover":              "the trust of every agent",
	"/sermon.web.v1.AgentCAService/RetireAgentCA":                     "the trust of every agent",
	"/sermon.web.v1.AlertService/AcknowledgeAlert":                    "security alerts: silences the reminders of an attack (other alerts need none)",
	"/sermon.web.v1.EnrollmentTokenService/CreateEnrollmentToken":     "lets machines join the organization",
	"/sermon.web.v1.LogSettingsService/SetGroupLogSettings":           "what agents send and redact (M3-25)",
	"/sermon.web.v1.LogSettingsService/ResetGroupLogSettings":         "what agents send and redact (M3-25)",
	"/sermon.web.v1.LogSettingsService/SetRedactionRules":             "redaction of secrets (M3-25)",
	"/sermon.web.v1.LogSettingsService/SetViewerLogAccess":            "who reads logs and security events (M3-25)",
	"/sermon.web.v1.LogSettingsService/SetLiveViewCommandLines":       "who sees command lines",
	"/sermon.web.v1.NodeService/RotateNodeKeys":                       "identities of nodes",
	"/sermon.web.v1.NodeService/UnlockNode":                           "identities of nodes",
	"/sermon.web.v1.NodeService/AllowNodeTakeover":                    "identities of nodes",
	"/sermon.web.v1.NodeService/SetNodeTags":                          "tags select alert rules and routing",
	"/sermon.web.v1.UpdatePolicyService/SetUpdatePolicy":              "installs updates and reboots nodes on its own (M4-10)",
	"/sermon.web.v1.UpdateService/StartUpdates":                       "installs updates and reboots nodes (M4-14)",
	"/sermon.web.v1.UpdateService/RebootNodes":                        "reboots nodes (M4-14)",
	"/sermon.web.v1.UpdateService/UpdateAgents":                       "replaces the agents of nodes (M4-19)",
	"/sermon.web.v1.UpdateService/CancelRollout":                      "stops a rollout of a group (M4-31)",
	"/sermon.web.v1.UpdateService/CancelRun":                          "withdraws a run from a node (M4-31)",
	"/sermon.web.v1.UpdatePolicyService/SetUpdatesPaused":             "stops or resumes every update of the organization (M4-31)",
	"/sermon.web.v1.UpdatePolicyService/ImportUpdatePolicy":           "installs updates and reboots nodes on its own (M4-10)",
	"/sermon.web.v1.UpdatePolicyService/ResetUpdatePolicy":            "stops the automatic updates of a group (M4-10)",
	"/sermon.web.v1.NotificationChannelService/CreateChannel":         "where alerts go",
	"/sermon.web.v1.NotificationChannelService/UpdateChannel":         "where alerts go",
	"/sermon.web.v1.NotificationChannelService/DeleteChannel":         "where alerts go",
	"/sermon.web.v1.NotificationPolicyService/CreatePolicy":           "where alerts go",
	"/sermon.web.v1.NotificationPolicyService/UpdatePolicy":           "where alerts go",
	"/sermon.web.v1.NotificationPolicyService/DeletePolicy":           "where alerts go",
	"/sermon.web.v1.MaintenanceWindowService/CreateMaintenanceWindow": "mutes alerts on a schedule",
	"/sermon.web.v1.MaintenanceWindowService/UpdateMaintenanceWindow": "mutes alerts on a schedule",
	"/sermon.web.v1.MaintenanceWindowService/DeleteMaintenanceWindow": "mutes alerts on a schedule",
	"/sermon.web.v1.OidcMappingService/CreateOidcMapping":             "rights from provider groups (M3-21)",
	"/sermon.web.v1.OidcMappingService/DeleteOidcMapping":             "rights from provider groups (M3-21)",
	"/sermon.web.v1.OrganizationService/CreateOrganization":           "instance: a new tenant with its admin",
	"/sermon.web.v1.OrganizationService/RenameOrganization":           "instance",
	"/sermon.web.v1.OrganizationService/UpdateOrganization":           "instance: quotas",
	"/sermon.web.v1.OrganizationService/LockOrganization":             "instance",
	"/sermon.web.v1.OrganizationService/ScheduleOrganizationDeletion": "instance",
	"/sermon.web.v1.OrganizationService/UnlockOrganization":           "instance",
	"/sermon.web.v1.OrganizationService/DeleteOrganization":           "instance",
	"/sermon.web.v1.OrganizationService/ExportOrganizationAudit":      "the whole audit log of a tenant (M3-25)",
	"/sermon.web.v1.PasskeyService/BeginPasskeyRegistration":          "a new way to sign in (M3-23)",
	"/sermon.web.v1.PasskeyService/FinishPasskeyRegistration":         "a new way to sign in (M3-23)",
	"/sermon.web.v1.PasskeyService/DeletePasskey":                     "a factor of the account (M3-23)",
	"/sermon.web.v1.TwoFactorService/BeginTotpSetup":                  "a new second factor",
	"/sermon.web.v1.TwoFactorService/RegenerateRecoveryCodes":         "codes that replace the second factor",
	"/sermon.web.v1.TwoFactorService/DisableTwoFactor":                "a factor of the account",
	"/sermon.web.v1.SecurityPolicyService/SetSecondFactorRequired":    "how members sign in",
	"/sermon.web.v1.AuthService/UnlinkOidcIdentity":                   "a way to sign in (M3-22)",
	"/sermon.web.v1.AlertRuleService/ImportAlertRules":                "what alerts at all: an attacker would silence the rules first",
	"/sermon.web.v1.AlertRuleService/SetAlertRuleEnabled":             "what alerts at all",
	"/sermon.web.v1.AlertRuleService/ResetAlertRule":                  "what alerts at all",
	"/sermon.web.v1.AlertSilenceService/CreateSilence":                "mutes alerts",
}

// stepUpExempt are the procedures that change something without a step-up, and why.
var stepUpExempt = map[string]string{
	"/sermon.web.v1.AccessService/CreateNodeGroup":                "an empty group gives nobody rights",
	"/sermon.web.v1.AccessService/RenameNodeGroup":                "the name only",
	"/sermon.web.v1.AccessService/RevokeInvitation":               "takes access away",
	"/sermon.web.v1.AlertSilenceService/ExpireSilence":            "ends a mute: alerts notify again",
	"/sermon.web.v1.AuthService/BeginOidc":                        "sign-in and step-up are the authentication; linking checks the step-up itself (oidc_integration_test.go)",
	"/sermon.web.v1.AuthService/BeginPasskey":                     "the authentication or the step-up itself",
	"/sermon.web.v1.AuthService/ConfirmPassword":                  "is the step-up",
	"/sermon.web.v1.AuthService/ConfirmSecondFactor":              "is the step-up",
	"/sermon.web.v1.AuthService/FinishPasskey":                    "the authentication or the step-up itself",
	"/sermon.web.v1.AuthService/Login":                            "the authentication itself",
	"/sermon.web.v1.AuthService/Logout":                           "ends the caller's session",
	"/sermon.web.v1.AuthService/SwitchOrganization":               "keeps the step-up time and gives no rights",
	"/sermon.web.v1.AuthService/VerifySecondFactor":               "the authentication itself",
	"/sermon.web.v1.EnrollmentTokenService/RevokeEnrollmentToken": "takes access away",
	"/sermon.web.v1.FleetService/GetFleetSummary":                 "reads only (no idempotency option)",
	"/sermon.web.v1.InvitationService/AcceptInvitation":           "by the invitee, proven by the link of the invitation",
	"/sermon.web.v1.InvitationService/JoinOrganization":           "by the invitee, proven by the link of the invitation",
	"/sermon.web.v1.MetricsService/ListMetrics":                   "reads only (no idempotency option)",
	"/sermon.web.v1.MetricsService/QueryMetrics":                  "reads only (no idempotency option)",
	"/sermon.web.v1.MetricsService/WatchMetrics":                  "reads only (a stream)",
	"/sermon.web.v1.NodeService/GetNode":                          "reads only (no idempotency option)",
	"/sermon.web.v1.NodeService/ListFilesystems":                  "reads only (no idempotency option)",
	"/sermon.web.v1.NotificationChannelService/TestChannel":       "sends a test message, changes no setting (manage_channels)",
	"/sermon.web.v1.PasskeyService/RenamePasskey":                 "the name only",
	"/sermon.web.v1.UpdateService/RefreshUpdateInfo":              "lists the updates again, changes nothing on the host (rate-limited)",
	"/sermon.web.v1.SecurityEventService/ListSecurityEvents":      "reads only (no idempotency option)",
	"/sermon.web.v1.SetupService/CompleteSetup":                   "first start, proven by the setup token",
	"/sermon.web.v1.TwoFactorService/ConfirmTotpSetup":            "finishes a setup begun with a step-up, proven with a code of the new secret",
}

// stepUpOwnCases call the required procedures that have no tenant-isolation case (they act on the
// caller's own organization or account).
var stepUpOwnCases = map[string]func(ctx context.Context, e stepUpEnv) error{
	// The tenant-isolation case only checks (dry run); storing needs the step-up.
	"/sermon.web.v1.AlertRuleService/ImportAlertRules": func(ctx context.Context, e stepUpEnv) error {
		_, err := e.rules.ImportAlertRules(ctx, connect.NewRequest(&webv1.ImportAlertRulesRequest{Yaml: "apiVersion: sermon/v1\n"}))
		return err
	},
	"/sermon.web.v1.LogSettingsService/SetRedactionRules": func(ctx context.Context, e stepUpEnv) error {
		_, err := e.logSettings.SetRedactionRules(ctx, connect.NewRequest(&webv1.SetRedactionRulesRequest{}))
		return err
	},
	"/sermon.web.v1.UpdatePolicyService/SetUpdatesPaused": func(ctx context.Context, e stepUpEnv) error {
		_, err := e.updatePolicies.SetUpdatesPaused(ctx, connect.NewRequest(&webv1.SetUpdatesPausedRequest{Paused: true}))
		return err
	},
	"/sermon.web.v1.LogSettingsService/SetViewerLogAccess": func(ctx context.Context, e stepUpEnv) error {
		_, err := e.logSettings.SetViewerLogAccess(ctx, connect.NewRequest(&webv1.SetViewerLogAccessRequest{ViewersSeeLogs: true}))
		return err
	},
	"/sermon.web.v1.LogSettingsService/SetLiveViewCommandLines": func(ctx context.Context, e stepUpEnv) error {
		_, err := e.logSettings.SetLiveViewCommandLines(ctx, connect.NewRequest(&webv1.SetLiveViewCommandLinesRequest{HideCommandLines: false}))
		return err
	},
	"/sermon.web.v1.PasskeyService/BeginPasskeyRegistration": func(ctx context.Context, e stepUpEnv) error {
		_, err := e.passkeys.BeginPasskeyRegistration(ctx, connect.NewRequest(&webv1.BeginPasskeyRegistrationRequest{Name: "Key"}))
		return err
	},
	"/sermon.web.v1.PasskeyService/FinishPasskeyRegistration": func(ctx context.Context, e stepUpEnv) error {
		_, err := e.passkeys.FinishPasskeyRegistration(ctx, connect.NewRequest(&webv1.FinishPasskeyRegistrationRequest{CeremonyId: "x", CredentialJson: "{}"}))
		return err
	},
	"/sermon.web.v1.PasskeyService/DeletePasskey": func(ctx context.Context, e stepUpEnv) error {
		_, err := e.passkeys.DeletePasskey(ctx, connect.NewRequest(&webv1.DeletePasskeyRequest{Id: "AAAAAAAAAAAAAAAAAAAAAA"}))
		return err
	},
	"/sermon.web.v1.TwoFactorService/BeginTotpSetup": func(ctx context.Context, e stepUpEnv) error {
		_, err := e.twoFactor.BeginTotpSetup(ctx, connect.NewRequest(&webv1.BeginTotpSetupRequest{}))
		return err
	},
	"/sermon.web.v1.TwoFactorService/RegenerateRecoveryCodes": func(ctx context.Context, e stepUpEnv) error {
		_, err := e.twoFactor.RegenerateRecoveryCodes(ctx, connect.NewRequest(&webv1.RegenerateRecoveryCodesRequest{}))
		return err
	},
	"/sermon.web.v1.TwoFactorService/DisableTwoFactor": func(ctx context.Context, e stepUpEnv) error {
		_, err := e.twoFactor.DisableTwoFactor(ctx, connect.NewRequest(&webv1.DisableTwoFactorRequest{}))
		return err
	},
	"/sermon.web.v1.SecurityPolicyService/SetSecondFactorRequired": func(ctx context.Context, e stepUpEnv) error {
		_, err := e.policy.SetSecondFactorRequired(ctx, connect.NewRequest(&webv1.SetSecondFactorRequiredRequest{Required: false}))
		return err
	},
	"/sermon.web.v1.AuthService/UnlinkOidcIdentity": func(ctx context.Context, e stepUpEnv) error {
		_, err := e.auth.UnlinkOidcIdentity(ctx, connect.NewRequest(&webv1.UnlinkOidcIdentityRequest{Issuer: "https://idp.example"}))
		return err
	},
}

// stepUpEnv are the clients of an admin of Org A for all groups (organization: a superadmin),
// whose step-up aged, and Org A's objects.
type stepUpEnv struct {
	isolationEnv
	twoFactor webv1connect.TwoFactorServiceClient
	policy    webv1connect.SecurityPolicyServiceClient
	passkeys  webv1connect.PasskeyServiceClient
}
