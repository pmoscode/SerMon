package ruleset_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/pmoscode/SerMon/server/internal/app/alerting"
	"github.com/pmoscode/SerMon/server/internal/app/alerting/ruleset"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

type overrides map[tenant.OrgID][]ruleset.Override

func (o overrides) Overrides(_ context.Context, org tenant.OrgID) ([]ruleset.Override, error) {
	if o == nil {
		return nil, errors.New("database down")
	}
	return o[org], nil
}

func mustParse(t *testing.T, yaml string) ruleset.Definition {
	t.Helper()
	def, err := ruleset.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return def
}

func document(t *testing.T, yaml string) []byte {
	t.Helper()
	data, err := ruleset.Marshal(mustParse(t, yaml).Doc)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

var memRule = strings.NewReplacer(
	"system.cpu.usage_high", "system.memory.usage_high", "category: cpu", "category: memory",
	"sermon_cpu_utilization_ratio", "sermon_memory_utilization_ratio", "  labels: { mode: idle }\n", "",
	`{ op: "<", value: 0.15 }`, `{ op: ">", value: 0.80 }`, `{ op: "<", value: 0.05 }`, `{ op: ">", value: 0.90 }`,
	`{ op: ">", value: 0.20 }`, `{ op: "<", value: 0.75 }`,
).Replace(cpuRule)

func ids(rules []alerting.Rule) []string {
	out := make([]string, len(rules))
	for i, r := range rules {
		out[i] = string(r.ID)
	}
	return out
}

func source(t *testing.T, store ruleset.OverrideStore, log *bytes.Buffer) *ruleset.Source {
	t.Helper()
	return ruleset.NewSource([]ruleset.Definition{mustParse(t, cpuRule), mustParse(t, memRule)}, store, slog.New(slog.NewTextHandler(log, nil)))
}

func TestSource_DefaultsWithoutOverrides(t *testing.T) {
	rules, err := source(t, overrides{}, &bytes.Buffer{}).Rules(context.Background(), orgA)
	if err != nil || strings.Join(ids(rules), ",") != "system.cpu.usage_high,system.memory.usage_high" {
		t.Errorf("rules = %v, %v", ids(rules), err)
	}
}

func TestSource_OverridesDisablesAndAddsPerOrganization(t *testing.T) {
	stricter := strings.NewReplacer(`{ op: "<", value: 0.15 }`, `{ op: "<", value: 0.30 }`, `{ op: ">", value: 0.20 }`, `{ op: ">", value: 0.35 }`).Replace(cpuRule)
	own := strings.NewReplacer("system.cpu.usage_high", "custom.cpu.steal_high", `labels: { mode: idle }`, `labels: { mode: steal }`,
		`{ op: "<", value: 0.15 }`, `{ op: ">", value: 0.15 }`, `{ op: "<", value: 0.05 }`, `{ op: ">", value: 0.30 }`,
		`{ op: ">", value: 0.20 }`, `{ op: "<", value: 0.10 }`).Replace(cpuRule)
	orgB := tenant.OrgID(group)
	store := overrides{orgA: {
		{Rule: "system.cpu.usage_high", Document: document(t, stricter)},
		{Rule: "system.memory.usage_high", Disabled: true},
		{Rule: "custom.cpu.steal_high", Document: document(t, own)},
	}}
	s := source(t, store, &bytes.Buffer{})
	rules, err := s.Rules(context.Background(), orgA)
	if err != nil || strings.Join(ids(rules), ",") != "system.cpu.usage_high,custom.cpu.steal_high" {
		t.Fatalf("rules = %v, %v", ids(rules), err)
	}
	if th := rules[0].Evaluator.(alerting.ThresholdRule).Thresholds.Warning.Value; th != 0.30 { //nolint:forcetypeassert // test
		t.Errorf("overridden warning = %v, want 0.30", th)
	}
	others, _ := s.Rules(context.Background(), orgB)
	if strings.Join(ids(others), ",") != "system.cpu.usage_high,system.memory.usage_high" {
		t.Errorf("another organization sees %v, want the defaults", ids(others))
	}
	if th := others[0].Evaluator.(alerting.ThresholdRule).Thresholds.Warning.Value; th != 0.15 { //nolint:forcetypeassert // test
		t.Errorf("the default changed: warning = %v", th)
	}
}

// A stored override that no longer loads (e.g. after a catalog change) must not leave the
// organization without the rule: the default applies and the problem is logged.
func TestSource_BrokenOverridesFallBackToTheDefault(t *testing.T) {
	var log bytes.Buffer
	store := overrides{orgA: {
		{Rule: "system.cpu.usage_high", Document: []byte(`{"apiVersion":"sermon/v1","kind":"AlertRule","spec":{"window":"x"}}`)},
		{Rule: "system.memory.usage_high", Document: document(t, cpuRule)}, // another rule's document
		{Rule: "custom.cpu.broken", Document: []byte(`{}`)},
	}}
	rules, err := source(t, store, &log).Rules(context.Background(), orgA)
	if err != nil || strings.Join(ids(rules), ",") != "system.cpu.usage_high,system.memory.usage_high" {
		t.Fatalf("rules = %v, %v", ids(rules), err)
	}
	if rules[1].Evaluator.(alerting.ThresholdRule).Series.Metric != "sermon_memory_utilization_ratio" { //nolint:forcetypeassert // test
		t.Error("the memory rule took the document of the cpu rule")
	}
	for _, want := range []string{"system.cpu.usage_high", "system.memory.usage_high", "custom.cpu.broken"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("log does not name %s:\n%s", want, log.String())
		}
	}
}

func TestSource_StoreErrors(t *testing.T) {
	if _, err := source(t, overrides(nil), &bytes.Buffer{}).Rules(context.Background(), orgA); err == nil {
		t.Error("Rules() without overrides succeeded; the engine must skip the round instead of using the defaults")
	}
}

func TestSource_DefinitionsKeepTheDocuments(t *testing.T) {
	defs, err := source(t, overrides{orgA: {{Rule: "system.memory.usage_high", Disabled: true}}}, &bytes.Buffer{}).Definitions(context.Background(), orgA)
	if err != nil || len(defs) != 2 || defs[0].Doc.GetMetadata().GetId() != "system.cpu.usage_high" || defs[0].Disabled || !defs[1].Disabled || defs[1].Overridden {
		t.Errorf("definitions = %+v, %v", defs, err)
	}
}
