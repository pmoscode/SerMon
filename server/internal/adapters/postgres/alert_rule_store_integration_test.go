//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/alerting"
	"github.com/pmoscode/SerMon/server/internal/app/alerting/ruleset"
	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

func TestAlertRuleStore_OverridesPerOrganization(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	store := postgres.NewAlertRuleStore(open(t, f.appDSN))
	defs, err := ruleset.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	doc, err := ruleset.Marshal(defs[0].Doc)
	if err != nil {
		t.Fatal(err)
	}
	alice := identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000a1"))
	audit := access.AuditEntry{Action: "alert_rule.import", Target: "alert_rules"}
	put := []ruleset.Override{
		{Rule: defs[0].Rule.ID, Document: doc, Source: []byte("# mine\n" + string(defs[0].Source))},
		{Rule: "system.swap.usage_high", Disabled: true},
	}
	if err := store.Apply(ctx, scopeFor(t, orgA), put, alice, audit); err != nil {
		t.Fatal(err)
	}
	// All or none: an override that changes nothing fails the whole batch.
	bad := []ruleset.Override{{Rule: "system.memory.usage_high", Disabled: true}, {Rule: "system.swap.usage_high"}}
	if err := store.Apply(ctx, scopeFor(t, orgA), bad, alice, audit); err == nil {
		t.Error("an override that changes nothing was stored")
	}

	got, err := store.Overrides(ctx, tenant.OrgID(orgA))
	if err != nil || len(got) != 2 {
		t.Fatalf("Overrides() = %+v, %v", got, err)
	}
	byRule := map[string]ruleset.Override{}
	for _, o := range got {
		byRule[string(o.Rule)] = o
	}
	stored := byRule[string(defs[0].Rule.ID)]
	if again, err := ruleset.Parse(stored.Document); err != nil || again.Rule.ID != defs[0].Rule.ID || stored.Disabled ||
		!strings.HasPrefix(string(stored.Source), "# mine\n") {
		t.Errorf("stored document reads back as %+v, %v", again.Rule, err)
	}
	if o := byRule["system.swap.usage_high"]; !o.Disabled || o.Document != nil || o.Source != nil {
		t.Errorf("disabled override = %+v", o)
	}
	if other, err := store.Overrides(ctx, tenant.OrgID(orgB)); err != nil || len(other) != 0 {
		t.Errorf("org B sees %+v, %v", other, err)
	}

	// Disable the overridden rule, keeping its document; then restore the default.
	if err := store.Apply(ctx, scopeFor(t, orgA), []ruleset.Override{{Rule: defs[0].Rule.ID, Disabled: true, Document: doc}}, identity.UserID{}, audit); err != nil {
		t.Fatal(err)
	}
	reset := access.AuditEntry{Action: "alert_rule.reset", Target: "alert_rule:x"}
	if err := store.Reset(ctx, scopeFor(t, orgB), defs[0].Rule.ID, reset); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("org B reset org A's override: %v", err)
	}
	for _, rule := range []alert.RuleID{defs[0].Rule.ID, "system.swap.usage_high"} {
		if err := store.Reset(ctx, scopeFor(t, orgA), rule, reset); err != nil {
			t.Errorf("Reset(%s) = %v", rule, err)
		}
	}
	if err := store.Reset(ctx, scopeFor(t, orgA), "system.swap.usage_high", reset); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("second reset: %v", err)
	}
	if left, _ := store.Overrides(ctx, tenant.OrgID(orgA)); len(left) != 0 {
		t.Errorf("overrides left: %+v", left)
	}
}

type noMetrics struct{}

func (noMetrics) Query(context.Context, tenant.Scope, metrics.Query) ([]metrics.Series, error) {
	return nil, nil
}

// The shipped rules reach the engine, and an organization's override takes effect in the next
// round: a node silent for two minutes is offline until the organization disables the rule.
func TestAlertEngine_EvaluatesTheDefaultRulesWithTheOverrides(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	org := uuid.New()
	ownerExec(t, f.db.OwnerDSN, "INSERT INTO organizations (id, name) VALUES ($1, 'Default rules ' || $1::uuid::text)", org)
	node := uuid.New()
	createNode(t, f.db.OwnerDSN, org, node)
	db := open(t, f.appDSN)
	if err := postgres.NewIngestNodeStore(db).Seen(ctx, tenant.OrgID(org), identity.NodeID(node), alertTime.Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	defaults, err := ruleset.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	rules := postgres.NewAlertRuleStore(db)
	alerts := postgres.NewAlertStore(db)
	engine := alerting.NewEngine(alerting.Deps{
		Store: alerts, Rules: ruleset.NewSource(defaults, rules, slog.New(slog.DiscardHandler)), Nodes: postgres.NewFleetStore(db),
		Metrics: noMetrics{}, Now: func() time.Time { return alertTime }, Logger: slog.New(slog.DiscardHandler),
	})
	offline := alert.Key{Rule: "system.node.offline", Node: identity.NodeID(node)}

	engine.Round(ctx)
	if list, _ := alerts.Instances(ctx, scopeFor(t, org)); !hasInstance(list, offline, alert.StateCritical) {
		t.Fatalf("instances = %+v, want node offline critical", list)
	}
	if err := rules.Apply(ctx, scopeFor(t, org), []ruleset.Override{{Rule: offline.Rule, Disabled: true}}, identity.UserID{}, access.AuditEntry{Action: "alert_rule.disable", Target: "alert_rule:x"}); err != nil {
		t.Fatal(err)
	}
	engine.Round(ctx)
	if list, _ := alerts.Instances(ctx, scopeFor(t, org)); hasInstance(list, offline, alert.StateCritical) {
		t.Errorf("instances = %+v, want the disabled rule's alert gone", list)
	}
}
