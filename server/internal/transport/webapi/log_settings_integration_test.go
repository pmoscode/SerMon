//go:build integration

package webapi_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
)

// Log settings over the real stack (M3-03, ADR 0030): admins only, invalid regexes rejected with
// the rule, each organization sees and changes only its own.
func TestLogSettings_AdminsSetGroupsAndRulesOfTheirOrganization(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ctx := context.Background()
	ann := login(t, s, "ann@example.org") // admin of Alpha
	mia := login(t, s, "mia@example.org") // viewer in Alpha

	if _, err := mia.logSettings.GetLogSettings(ctx, connect.NewRequest(&webv1.GetLogSettingsRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("viewer reads the settings: %v", err)
	}
	before, err := ann.logSettings.GetLogSettings(ctx, connect.NewRequest(&webv1.GetLogSettingsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, g := range before.Msg.GetGroups() {
		if g.GetGroupId() == groupAl {
			found = true
		}
	}
	if !found || before.Msg.GetVersion() < 1 {
		t.Fatalf("GetLogSettings() = %v", before.Msg)
	}

	settings := &webv1.LogSettings{
		Enabled: true, MinSeverity: webv1.LogSeverity_LOG_SEVERITY_WARNING, ExcludeUnits: []string{"cron.service"},
		Files: []*webv1.LogFileGlob{{Pattern: "/var/log/nginx/*.log"}},
	}
	if _, err := ann.logSettings.SetGroupLogSettings(ctx, connect.NewRequest(&webv1.SetGroupLogSettingsRequest{GroupId: groupAl, Settings: settings})); err != nil {
		t.Fatalf("SetGroupLogSettings() = %v", err)
	}
	t.Cleanup(func() {
		_, _ = ann.logSettings.ResetGroupLogSettings(ctx, connect.NewRequest(&webv1.ResetGroupLogSettingsRequest{GroupId: groupAl}))
		_, _ = ann.logSettings.SetRedactionRules(ctx, connect.NewRequest(&webv1.SetRedactionRulesRequest{}))
	})
	_, err = ann.logSettings.SetRedactionRules(ctx, connect.NewRequest(&webv1.SetRedactionRulesRequest{Rules: []*webv1.RedactionRule{
		{Pattern: `customer-\d{6}`}, {Pattern: `(?<!x)y`},
	}}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "rule 2") {
		t.Errorf("look-behind: %v, want INVALID_ARGUMENT naming rule 2", err)
	}
	if _, err := ann.logSettings.SetRedactionRules(ctx, connect.NewRequest(&webv1.SetRedactionRulesRequest{Rules: []*webv1.RedactionRule{{Pattern: `customer-\d{6}`}}})); err != nil {
		t.Fatal(err)
	}
	after, err := ann.logSettings.GetLogSettings(ctx, connect.NewRequest(&webv1.GetLogSettingsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if after.Msg.GetVersion() != before.Msg.GetVersion()+2 || len(after.Msg.GetRedactionRules()) != 1 {
		t.Errorf("after the changes: %v (before version %d)", after.Msg, before.Msg.GetVersion())
	}
	for _, g := range after.Msg.GetGroups() {
		if g.GetGroupId() == groupAl && (!g.GetCustom() || g.GetSettings().GetMinSeverity() != webv1.LogSeverity_LOG_SEVERITY_WARNING || g.GetSettings().GetFiles()[0].GetPattern() != "/var/log/nginx/*.log") {
			t.Errorf("Alpha's group = %v", g)
		}
	}

	// Beta's admin sees nothing of Alpha and changes only Beta.
	bea := login(t, s, "bea@example.org")
	beta, err := bea.logSettings.GetLogSettings(ctx, connect.NewRequest(&webv1.GetLogSettingsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(beta.Msg.String(), groupAl) || strings.Contains(beta.Msg.String(), "customer-") {
		t.Errorf("Beta sees Alpha's settings: %v", beta.Msg)
	}
	if _, err := bea.logSettings.SetRedactionRules(ctx, connect.NewRequest(&webv1.SetRedactionRulesRequest{})); err != nil {
		t.Errorf("Beta changes its own rules: %v", err)
	}
	if again, _ := ann.logSettings.GetLogSettings(ctx, connect.NewRequest(&webv1.GetLogSettingsRequest{})); len(again.Msg.GetRedactionRules()) != 1 {
		t.Errorf("Alpha's rules after Beta's change = %v", again.Msg.GetRedactionRules())
	}
}

// The viewer switch over the real stack (M3-12, T22): it takes effect with the next request of a
// running session, because the principal is loaded per request; only admins with a step-up set it.
func TestLogSettings_TheViewerSwitchTakesEffectAtOnce(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ctx := context.Background()
	ann := login(t, s, "ann@example.org") // admin of Alpha
	mia := login(t, s, "mia@example.org") // viewer in Alpha
	query := func() error {
		_, err := mia.logs.QueryLogs(ctx, connect.NewRequest(&webv1.QueryLogsRequest{Filter: &webv1.LogFilter{
			From: timestamppb.New(time.Now().Add(-time.Hour)), To: timestamppb.Now(),
		}}))
		return err
	}
	if err := query(); err != nil {
		t.Fatalf("viewer with the switch on: %v", err)
	}
	if _, err := mia.logSettings.SetViewerLogAccess(ctx, connect.NewRequest(&webv1.SetViewerLogAccessRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("a viewer sets the switch: %v", err)
	}
	if _, err := ann.logSettings.SetViewerLogAccess(ctx, connect.NewRequest(&webv1.SetViewerLogAccessRequest{})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = ann.logSettings.SetViewerLogAccess(ctx, connect.NewRequest(&webv1.SetViewerLogAccessRequest{ViewersSeeLogs: true}))
	})
	if err := query(); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("viewer after the switch: %v, want PERMISSION_DENIED", err)
	}
	settings, err := ann.logSettings.GetLogSettings(ctx, connect.NewRequest(&webv1.GetLogSettingsRequest{}))
	if err != nil || settings.Msg.GetViewersSeeLogs() {
		t.Errorf("GetLogSettings() = %v, %v", settings, err)
	}
	if _, err := ann.logSettings.SetViewerLogAccess(ctx, connect.NewRequest(&webv1.SetViewerLogAccessRequest{ViewersSeeLogs: true})); err != nil {
		t.Fatal(err)
	}
	if err := query(); err != nil {
		t.Errorf("viewer after switching back: %v", err)
	}

	// The node status lists Alpha's node only; Beta's admin sees none of it.
	var alpha bool
	for _, n := range settings.Msg.GetNodes() {
		alpha = alpha || n.GetNodeId() == nodeAl
	}
	if !alpha {
		t.Errorf("nodes = %v", settings.Msg.GetNodes())
	}
	bea := login(t, s, "bea@example.org")
	beta, err := bea.logSettings.GetLogSettings(ctx, connect.NewRequest(&webv1.GetLogSettingsRequest{}))
	if err != nil || strings.Contains(beta.Msg.String(), nodeAl) || !beta.Msg.GetViewersSeeLogs() {
		t.Errorf("Beta: %v, %v", beta, err)
	}

	// The test of rules shows what an agent would send, without saving anything.
	tested, err := ann.logSettings.TestRedaction(ctx, connect.NewRequest(&webv1.TestRedactionRequest{
		Rules: []*webv1.RedactionRule{{Pattern: `customer-\d{6}`}}, Sample: "token=abc for customer-123456",
	}))
	if err != nil || tested.Msg.GetRedacted() != "token=*** for ***" {
		t.Errorf("TestRedaction() = %v, %v", tested, err)
	}
	_, err = ann.logSettings.TestRedaction(ctx, connect.NewRequest(&webv1.TestRedactionRequest{Rules: []*webv1.RedactionRule{{Pattern: "(unclosed"}}}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "rule 1") {
		t.Errorf("a bad rule: %v", err)
	}
}
