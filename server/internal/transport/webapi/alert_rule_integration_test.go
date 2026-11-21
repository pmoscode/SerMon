//go:build integration

package webapi_test

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"

	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
)

func TestAlertRuleAPI_EditImportExport(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	seedChannelAlpha(t) // Alpha switched the swap rule off
	ctx := context.Background()
	ann := login(t, s, "ann@example.org")

	list, err := ann.rules.ListAlertRules(ctx, connect.NewRequest(&webv1.ListAlertRulesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	var cpu string
	for _, r := range list.Msg.GetRules() {
		switch r.GetId() {
		case "system.swap.usage_high":
			if r.GetEnabled() || r.GetOrigin() != webv1.AlertRuleOrigin_ALERT_RULE_ORIGIN_DEFAULT {
				t.Errorf("swap %v", r)
			}
		case "system.cpu.usage_high":
			cpu = r.GetYaml()
			if r.GetHelp().GetEn() == "" || r.GetName().GetEn() == "" {
				t.Errorf("cpu %v", r)
			}
		}
	}
	changed := strings.Replace(cpu, "window: 10m", "window: 15m", 1)
	dry, err := ann.rules.ImportAlertRules(ctx, connect.NewRequest(&webv1.ImportAlertRulesRequest{Yaml: changed, DryRun: true}))
	if err != nil || dry.Msg.GetApplied() || len(dry.Msg.GetChanges()) != 1 ||
		dry.Msg.GetChanges()[0].GetAction() != webv1.AlertRuleChangeAction_ALERT_RULE_CHANGE_ACTION_UPDATE || dry.Msg.GetChanges()[0].GetBeforeYaml() != cpu {
		t.Fatalf("dry run %v, %v", dry, err)
	}
	// No free queries: an unknown field is refused with its line.
	bad, err := ann.rules.ImportAlertRules(ctx, connect.NewRequest(&webv1.ImportAlertRulesRequest{Yaml: strings.Replace(cpu, "spec:\n", "spec:\n  query: up\n", 1)}))
	if err != nil || bad.Msg.GetApplied() || len(bad.Msg.GetProblems()) != 1 || !strings.Contains(bad.Msg.GetProblems()[0].GetMessage(), `unknown field "query"`) {
		t.Errorf("free query %v, %v", bad, err)
	}
	applied, err := ann.rules.ImportAlertRules(ctx, connect.NewRequest(&webv1.ImportAlertRulesRequest{Yaml: changed}))
	if err != nil || !applied.Msg.GetApplied() {
		t.Fatalf("import %v, %v", applied, err)
	}
	export, err := ann.rules.ExportAlertRules(ctx, connect.NewRequest(&webv1.ExportAlertRulesRequest{}))
	if err != nil || !strings.Contains(export.Msg.GetYaml(), "window: 15m") || strings.Count(export.Msg.GetYaml(), "\n---\n")+strings.Count(export.Msg.GetYaml(), "---\n") == 0 {
		t.Errorf("export %v, %v", export, err)
	}
	if _, err := ann.rules.SetAlertRuleEnabled(ctx, connect.NewRequest(&webv1.SetAlertRuleEnabledRequest{RuleId: "system.swap.usage_high", Enabled: true})); err != nil {
		t.Errorf("enable: %v", err)
	}
	if _, err := ann.rules.ResetAlertRule(ctx, connect.NewRequest(&webv1.ResetAlertRuleRequest{RuleId: "system.cpu.usage_high"})); err != nil {
		t.Errorf("reset: %v", err)
	}
	var overrides int
	ownerQuery(t, "SELECT count(*) FROM alert_rule_overrides WHERE org_id = '"+orgAlpha+"'", &overrides)
	if overrides != 0 {
		t.Errorf("%d overrides left", overrides)
	}

	// Viewers read but don't change; an old login steps up first.
	mia := login(t, s, "mia@example.org")
	if _, err := mia.rules.ListAlertRules(ctx, connect.NewRequest(&webv1.ListAlertRulesRequest{})); err != nil {
		t.Errorf("viewer lists: %v", err)
	}
	if _, err := mia.rules.ImportAlertRules(ctx, connect.NewRequest(&webv1.ImportAlertRulesRequest{Yaml: changed, DryRun: true})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("viewer imports: %v", err)
	}
	ownerSQL(t, "UPDATE sessions SET step_up_at = now() - interval '1 hour' WHERE user_id = '"+annID+"'")
	if _, err := ann.rules.SetAlertRuleEnabled(ctx, connect.NewRequest(&webv1.SetAlertRuleEnabledRequest{RuleId: "system.swap.usage_high"})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("disable with an old login: %v", err)
	}
}
