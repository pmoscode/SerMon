package rules_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/alerting/ruleset"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/rules"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	t0     = time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	orgA   = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
	orgB   = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000b"))
	groupA = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000a1"))
	ann    = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000e1"))
)

const cpu alert.RuleID = "system.cpu.usage_high"

// store keeps overrides per organization, like the table with RLS.
type store struct {
	byOrg  map[tenant.OrgID][]ruleset.Override
	audits []access.AuditEntry
}

func (s *store) Overrides(_ context.Context, org tenant.OrgID) ([]ruleset.Override, error) {
	return slices.Clone(s.byOrg[org]), nil
}

func (s *store) Apply(_ context.Context, scope tenant.Scope, put []ruleset.Override, _ identity.UserID, a access.AuditEntry) error {
	if s.byOrg == nil {
		s.byOrg = map[tenant.OrgID][]ruleset.Override{}
	}
	list := s.byOrg[scope.OrgID()]
	for _, o := range put {
		if !o.Disabled && o.Document == nil {
			return errors.New("an override must change something")
		}
		list = slices.DeleteFunc(list, func(x ruleset.Override) bool { return x.Rule == o.Rule })
		list = append(list, o)
	}
	s.byOrg[scope.OrgID()] = list
	s.audits = append(s.audits, a)
	return nil
}

func (s *store) Reset(_ context.Context, scope tenant.Scope, rule alert.RuleID, a access.AuditEntry) error {
	list := s.byOrg[scope.OrgID()]
	i := slices.IndexFunc(list, func(x ruleset.Override) bool { return x.Rule == rule })
	if i < 0 {
		return access.ErrNotFound
	}
	s.byOrg[scope.OrgID()] = slices.Delete(list, i, i+1)
	s.audits = append(s.audits, a)
	return nil
}

func service(t *testing.T, s *store, now time.Time) *rules.Service {
	t.Helper()
	defaults, err := ruleset.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	return rules.NewService(rules.Deps{Defaults: defaults, Store: s, Policy: authz.NewPolicy(), Now: func() time.Time { return now }})
}

func caller(org tenant.OrgID, b authz.Binding, stepUp time.Time) access.Caller {
	return access.Caller{Principal: authz.Principal{User: ann, Org: org, Bindings: []authz.Binding{b}}, StepUpAt: stepUp}
}

func admin() access.Caller { return caller(orgA, authz.Binding{AllGroups: true, Role: role.Admin}, t0) }

func source(t *testing.T, id alert.RuleID) string {
	t.Helper()
	defaults, _ := ruleset.Defaults()
	for _, d := range defaults {
		if d.Rule.ID == id {
			return string(d.Source)
		}
	}
	t.Fatalf("no default %s", id)
	return ""
}

func find(list []rules.Rule, id alert.RuleID) rules.Rule {
	for _, r := range list {
		if r.ID == id {
			return r
		}
	}
	return rules.Rule{}
}

func TestList_DefaultsWithTheirText(t *testing.T) {
	list, err := service(t, &store{}, t0).List(context.Background(), caller(orgA, authz.Binding{Group: groupA, Role: role.Viewer}, time.Time{}))
	if err != nil {
		t.Fatal(err)
	}
	r := find(list, cpu)
	if r.Origin != rules.Default || !r.Enabled || r.Type != "threshold" || r.YAML != source(t, cpu) || r.DefaultYAML != "" || r.Doc == nil {
		t.Errorf("cpu %+v", r)
	}
	if _, err := service(t, &store{}, t0).List(context.Background(), access.Caller{Principal: authz.Principal{User: ann, Org: orgA}}); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("without membership: %v", err)
	}
}

// A dry run shows what would change; nothing is stored. Applying stores the documents with
// their text and one audit entry.
func TestImport_DryRunThenApply(t *testing.T) {
	s := &store{}
	svc := service(t, s, t0)
	changed := "# mine\n" + strings.Replace(source(t, cpu), "window: 10m", "window: 15m", 1)
	own := strings.Replace(source(t, cpu), "id: system.cpu.usage_high", "id: system.cpu.usage_very_high", 1)
	bundle := changed + "---\n" + own + "---\n" + source(t, "system.swap.usage_high")

	res, err := svc.Import(context.Background(), admin(), []byte(bundle), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Problems) != 0 || res.Applied || len(s.byOrg[orgA]) != 0 {
		t.Fatalf("dry run %+v", res)
	}
	actions := map[alert.RuleID]rules.Action{}
	for _, c := range res.Changes {
		actions[c.Rule] = c.Action
	}
	if actions[cpu] != rules.Update || actions["system.cpu.usage_very_high"] != rules.Create || actions["system.swap.usage_high"] != rules.Unchanged {
		t.Errorf("actions %v", actions)
	}
	if c := res.Changes[0]; c.Before != source(t, cpu) || c.After != changed {
		t.Errorf("diff of cpu: %+v", c)
	}

	res, err = svc.Import(context.Background(), admin(), []byte(bundle), false)
	if err != nil || !res.Applied {
		t.Fatalf("apply %+v, %v", res, err)
	}
	if len(s.byOrg[orgA]) != 2 || len(s.audits) != 1 || s.audits[0].Action != "alert_rule.import" {
		t.Errorf("stored %+v, audits %+v", s.byOrg[orgA], s.audits)
	}
	list, _ := svc.List(context.Background(), admin())
	if r := find(list, cpu); r.Origin != rules.Changed || r.YAML != changed || r.DefaultYAML != source(t, cpu) {
		t.Errorf("changed cpu %+v", r)
	}
	if r := find(list, "system.cpu.usage_very_high"); r.Origin != rules.Own || !r.Enabled {
		t.Errorf("own rule %+v", r)
	}
}

// An organization holds at most MaxOwnRules rules of its own (M2-38): its rules are evaluated
// next to every other organization's. Changing a shipped rule doesn't count.
func TestImport_BoundsTheOwnRules(t *testing.T) {
	s := &store{}
	svc := service(t, s, t0)
	ownRule := func(i int) string {
		return strings.Replace(source(t, cpu), "id: system.cpu.usage_high", fmt.Sprintf("id: acme.cpu.high_%03d", i), 1)
	}
	importN := func(from, to int) (rules.Result, error) {
		var docs []string
		for i := from; i < to; i++ {
			docs = append(docs, ownRule(i))
		}
		return svc.Import(context.Background(), admin(), []byte(strings.Join(docs, "---\n")), false)
	}
	for from := 0; from < rules.MaxOwnRules; from += rules.MaxImportDocuments {
		if res, err := importN(from, min(from+rules.MaxImportDocuments, rules.MaxOwnRules)); err != nil || !res.Applied {
			t.Fatalf("import from %d: %+v, %v", from, res.Problems, err)
		}
	}
	res, err := importN(rules.MaxOwnRules, rules.MaxOwnRules+1)
	if err != nil || res.Applied || len(res.Problems) != 1 || !strings.Contains(res.Problems[0].Message, fmt.Sprintf("at most %d rules of its own", rules.MaxOwnRules)) {
		t.Errorf("rule %d: %+v, %v", rules.MaxOwnRules+1, res, err)
	}
	// Updating an existing own rule and changing a shipped one still work.
	changed := strings.Replace(ownRule(0), "window: 10m", "window: 15m", 1) + "---\n" + strings.Replace(source(t, cpu), "window: 10m", "window: 15m", 1)
	if res, err := svc.Import(context.Background(), admin(), []byte(changed), false); err != nil || !res.Applied {
		t.Errorf("update at the limit: %+v, %v", res.Problems, err)
	}
}

// One bad document stops the whole import; the errors name document and line; there are no
// free queries.
func TestImport_RejectsAllOnAnError(t *testing.T) {
	s := &store{}
	svc := service(t, s, t0)
	query := strings.Replace(source(t, cpu), "spec:\n", "spec:\n  query: \"rate(node_cpu[5m])\"\n", 1)
	// The first document is a valid change: it is not stored either.
	swap := strings.Replace(source(t, "system.swap.usage_high"), "window: ", "window: 1", 1)
	if swap == source(t, "system.swap.usage_high") {
		t.Fatal("the swap rule has no window to change")
	}
	bundle := swap + "---\n" + query + "---\n" + source(t, "system.swap.usage_high")
	res, err := svc.Import(context.Background(), admin(), []byte(bundle), false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied || len(s.byOrg[orgA]) != 0 || len(res.Problems) != 2 {
		t.Fatalf("result %+v", res)
	}
	p := res.Problems[0]
	swapLines := strings.Count(swap, "\n")
	head, _, found := strings.Cut(source(t, cpu), "spec:\n")
	if !found {
		t.Fatal("the cpu rule has no spec")
	}
	cpuSpec := strings.Count(head, "\n") + 1
	if p.Document != 2 || p.Line != swapLines+2 || !strings.Contains(p.Message, `unknown field "query"`) || !strings.Contains(p.Message, "line "+strconv.Itoa(swapLines+1+cpuSpec+1)) {
		t.Errorf("problem %+v", p)
	}
	if q := res.Problems[1]; q.Document != 3 || !strings.Contains(q.Message, "twice") {
		t.Errorf("duplicate %+v", q)
	}
	if res, _ := svc.Import(context.Background(), admin(), []byte("# nothing\n"), true); len(res.Problems) != 1 {
		t.Errorf("empty import %+v", res)
	}
}

func TestEnableDisableAndReset(t *testing.T) {
	s := &store{}
	svc := service(t, s, t0)
	ctx := context.Background()
	if err := svc.SetEnabled(ctx, admin(), cpu, false); err != nil {
		t.Fatal(err)
	}
	list, _ := svc.List(ctx, admin())
	if r := find(list, cpu); r.Enabled || r.Origin != rules.Default {
		t.Errorf("disabled default %+v", r)
	}
	// Enabling a default without its own document removes the override.
	if err := svc.SetEnabled(ctx, admin(), cpu, true); err != nil || len(s.byOrg[orgA]) != 0 {
		t.Errorf("enable: %v, %+v", err, s.byOrg[orgA])
	}
	if err := svc.SetEnabled(ctx, admin(), "system.cpu.unknown_rule", false); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("unknown rule: %v", err)
	}
	changed := strings.Replace(source(t, cpu), "window: 10m", "window: 15m", 1)
	if _, err := svc.Import(ctx, admin(), []byte(changed), false); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetEnabled(ctx, admin(), cpu, false); err != nil {
		t.Fatal(err)
	}
	if o := s.byOrg[orgA][0]; !o.Disabled || o.Document == nil {
		t.Errorf("disabling keeps the document: %+v", o)
	}
	// Importing a new version keeps the rule switched off.
	if _, err := svc.Import(ctx, admin(), []byte(strings.Replace(changed, "window: 15m", "window: 20m", 1)), false); err != nil {
		t.Fatal(err)
	}
	if o := s.byOrg[orgA][0]; !o.Disabled || !strings.Contains(string(o.Source), "window: 20m") {
		t.Errorf("import switched the rule on: %+v", o)
	}
	if err := svc.Reset(ctx, admin(), cpu); err != nil {
		t.Fatal(err)
	}
	if err := svc.Reset(ctx, admin(), cpu); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("second reset: %v", err)
	}
	var actions []string
	for _, a := range s.audits {
		actions = append(actions, a.Action)
	}
	if !slices.Equal(actions, []string{"alert_rule.disable", "alert_rule.enable", "alert_rule.import", "alert_rule.disable", "alert_rule.import", "alert_rule.reset"}) {
		t.Errorf("audit %v", actions)
	}
}

// Rules decide what alerts at all: only admins change them, with a fresh step-up; a dry run
// needs no step-up.
func TestRightsAndStepUp(t *testing.T) {
	svc := service(t, &store{}, t0)
	ctx := context.Background()
	op := caller(orgA, authz.Binding{AllGroups: true, Role: role.Operator}, t0)
	if _, err := svc.Import(ctx, op, []byte(source(t, cpu)), true); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("operator imports: %v", err)
	}
	if err := svc.SetEnabled(ctx, op, cpu, false); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("operator disables: %v", err)
	}
	stale := caller(orgA, authz.Binding{AllGroups: true, Role: role.Admin}, t0.Add(-time.Hour))
	if _, err := svc.Import(ctx, stale, []byte(source(t, cpu)), true); err != nil {
		t.Errorf("dry run without step-up: %v", err)
	}
	if _, err := svc.Import(ctx, stale, []byte(source(t, cpu)), false); !errors.Is(err, access.ErrStepUpRequired) {
		t.Errorf("import without step-up: %v", err)
	}
	if err := svc.SetEnabled(ctx, stale, cpu, false); !errors.Is(err, access.ErrStepUpRequired) {
		t.Errorf("disable without step-up: %v", err)
	}
	if err := svc.Reset(ctx, stale, cpu); !errors.Is(err, access.ErrStepUpRequired) {
		t.Errorf("reset without step-up: %v", err)
	}
}

func TestExport_ChangedOrAll(t *testing.T) {
	s := &store{}
	svc := service(t, s, t0)
	ctx := context.Background()
	changed := strings.Replace(source(t, cpu), "window: 10m", "window: 15m", 1)
	if _, err := svc.Import(ctx, admin(), []byte(changed), false); err != nil {
		t.Fatal(err)
	}
	only, err := svc.Export(ctx, caller(orgA, authz.Binding{Group: groupA, Role: role.Viewer}, time.Time{}), false)
	if err != nil {
		t.Fatal(err)
	}
	docs := ruleset.ParseBundle([]byte(only))
	if len(docs) != 1 || docs[0].Err != nil || docs[0].Def.Rule.ID != cpu || !strings.Contains(only, "window: 15m") {
		t.Errorf("export of changes:\n%s", only)
	}
	everything, _ := svc.Export(ctx, admin(), true)
	defaults, _ := ruleset.Defaults()
	if docs := ruleset.ParseBundle([]byte(everything)); len(docs) != len(defaults) {
		t.Errorf("%d documents for %d rules", len(docs), len(defaults))
	}
	// Another organization exports the shipped rules only.
	other, _ := svc.Export(ctx, caller(orgB, authz.Binding{AllGroups: true, Role: role.Admin}, t0), false)
	if strings.TrimSpace(strings.SplitN(other, "\n", 2)[1]) != "" {
		t.Errorf("org B exports %q", other)
	}
}
