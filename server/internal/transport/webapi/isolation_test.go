package webapi_test

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	_ "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1" // register the web API descriptors
)

// tenantIsolation lists every web API procedure. Each one either has a cross-tenant test in
// tenantIsolationCases (integration tests: a user of Org B must get NOT_FOUND or PERMISSION_DENIED
// for Org A data) or a documented reason why it handles no tenant data.
var tenantIsolationExempt = map[string]string{
	"/sermon.web.v1.AuthService/Login":                             "acts on the caller's own credentials, no tenant data",
	"/sermon.web.v1.AuthService/Logout":                            "ends the caller's own session",
	"/sermon.web.v1.AuthService/GetSession":                        "describes the caller's own session",
	"/sermon.web.v1.AuthService/ConfirmPassword":                   "re-authenticates the caller's own session",
	"/sermon.web.v1.SystemService/GetVersion":                      "instance information, no tenant data (requires authentication)",
	"/sermon.web.v1.SetupService/GetSetupStatus":                   "only reports whether the instance is set up, no tenant data",
	"/sermon.web.v1.SetupService/CompleteSetup":                    "creates the first organization; works once with the setup token",
	"/sermon.web.v1.InvitationService/GetInvitation":               "acts only on the invitation named by the secret link token (ADR 0008)",
	"/sermon.web.v1.InvitationService/AcceptInvitation":            "creates a new account for the invitation named by the secret link token (ADR 0008)",
	"/sermon.web.v1.MetricsService/ListMetrics":                    "the static metric catalog, no tenant data (requires authentication)",
	"/sermon.web.v1.NotificationChannelService/ListChannelTypes":   "the static channel types of the server, no tenant data (manage_channels, app/notify)",
	"/sermon.web.v1.InstanceAuditService/ListInstanceAuditLog":     "instance-level log for the superadmin only, no tenant data (role test in app/instanceaudit)",
	"/sermon.web.v1.AuthService/VerifySecondFactor":                "completes the caller's own login",
	"/sermon.web.v1.AuthService/ConfirmSecondFactor":               "re-authenticates the caller's own session",
	"/sermon.web.v1.AuthService/GetLoginOptions":                   "names the instance's sign-in ways, no tenant data",
	"/sermon.web.v1.AuthService/ListOidcIdentities":                "lists the caller's own identities",
	"/sermon.web.v1.AuthService/UnlinkOidcIdentity":                "unlinks the caller's own identity",
	"/sermon.web.v1.AuthService/BeginPasskey":                      "the caller's own sign-in, second factor or step-up",
	"/sermon.web.v1.AuthService/FinishPasskey":                     "the caller's own sign-in, second factor or step-up",
	"/sermon.web.v1.PasskeyService/ListPasskeys":                   "the caller's own passkeys, instance-level",
	"/sermon.web.v1.PasskeyService/BeginPasskeyRegistration":       "the caller's own passkeys, instance-level",
	"/sermon.web.v1.PasskeyService/FinishPasskeyRegistration":      "the caller's own passkeys, instance-level",
	"/sermon.web.v1.PasskeyService/RenamePasskey":                  "the caller's own passkeys, instance-level",
	"/sermon.web.v1.PasskeyService/DeletePasskey":                  "the caller's own passkeys, instance-level",
	"/sermon.web.v1.AuthService/BeginOidc":                         "starts the caller's own sign-in, link or step-up at the instance's provider",
	"/sermon.web.v1.TwoFactorService/GetTwoFactorStatus":           "the caller's own second factor (per user, no tenant data)",
	"/sermon.web.v1.TwoFactorService/BeginTotpSetup":               "the caller's own second factor (per user, no tenant data)",
	"/sermon.web.v1.TwoFactorService/ConfirmTotpSetup":             "the caller's own second factor (per user, no tenant data)",
	"/sermon.web.v1.TwoFactorService/RegenerateRecoveryCodes":      "the caller's own second factor (per user, no tenant data)",
	"/sermon.web.v1.TwoFactorService/DisableTwoFactor":             "the caller's own second factor (per user, no tenant data)",
	"/sermon.web.v1.SecurityPolicyService/GetSecurityPolicy":       "only the caller's own organization, no IDs (two_factor_integration_test.go)",
	"/sermon.web.v1.SecurityPolicyService/SetSecondFactorRequired": "only the caller's own organization, no IDs (two_factor_integration_test.go)",
	"/sermon.web.v1.LogSettingsService/GetLogSettings":             "only the caller's own organization, no IDs (log_settings_integration_test.go)",
	"/sermon.web.v1.LogSettingsService/SetRedactionRules":          "only the caller's own organization, no IDs (log_settings_integration_test.go)",
	"/sermon.web.v1.LogSettingsService/SetViewerLogAccess":         "only the caller's own organization, no IDs (log_settings_integration_test.go)",
	"/sermon.web.v1.LogSettingsService/SetLiveViewCommandLines":    "only the caller's own organization, no IDs (live_view_integration_test.go)",
	"/sermon.web.v1.LogSettingsService/TestRedaction":              "no stored data, no IDs: redacts the request's own sample",
	"/sermon.web.v1.ModuleService/ListModules":                     "the static module catalog of the agent's build, no tenant data (requires a role)",
	"/sermon.web.v1.UpdatePolicyService/SetUpdatesPaused":          "only the caller's own organization, no IDs (update_integration_test.go)",
}

// TestTenantIsolation_EveryProcedureIsCovered fails when a new RPC is added without a
// cross-tenant test or an explicit exemption (KONZEPT §18.1, skill sermon-security-review).
func TestTenantIsolation_EveryProcedureIsCovered(t *testing.T) {
	var procedures []string
	protoregistry.GlobalFiles.RangeFilesByPackage("sermon.web.v1", func(fd protoreflect.FileDescriptor) bool {
		for i := range fd.Services().Len() {
			svc := fd.Services().Get(i)
			for j := range svc.Methods().Len() {
				procedures = append(procedures, "/"+string(svc.FullName())+"/"+string(svc.Methods().Get(j).Name()))
			}
		}
		return true
	})
	if len(procedures) == 0 {
		t.Fatal("no web API procedures found – registry not loaded")
	}
	for _, p := range procedures {
		_, exempt := tenantIsolationExempt[p]
		_, covered := tenantIsolationCases[p]
		switch {
		case exempt && covered:
			t.Errorf("%s is both exempt and covered – pick one", p)
		case !exempt && !covered:
			t.Errorf("%s has no tenant-isolation test (add it to tenantIsolationCases) and no exemption", p)
		}
		if reason := tenantIsolationExempt[p]; exempt && strings.TrimSpace(reason) == "" {
			t.Errorf("%s: exemption needs a reason", p)
		}
	}
}
