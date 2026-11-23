//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/logconfig"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Log settings and rules (M3-03, ADR 0030): per group and organization, every change raises the
// version, and the configuration of a node combines its group with its organization.
func TestLogSettingsStore_ConfigurationOfANode(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	store := postgres.NewLogSettingsStore(open(t, f.appDSN))
	node := uuid.New()
	createNode(t, f.db.OwnerDSN, orgA, node)
	var group uuid.UUID
	queryOwner(t, f.db.OwnerDSN, `SELECT group_id FROM nodes WHERE id = $1`, []any{node}, &group)
	alice := identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000a1"))
	version := func() uint64 {
		t.Helper()
		config, err := store.ForNode(ctx, tenant.OrgID(orgA), identity.NodeID(node))
		if err != nil {
			t.Fatal(err)
		}
		return config.Version
	}
	start := version()

	config, err := store.ForNode(ctx, tenant.OrgID(orgA), identity.NodeID(node))
	if err != nil || !config.Settings.Enabled || len(config.Settings.ExcludeUnits) != 0 {
		t.Fatalf("ForNode() = %+v, %v; want the defaults", config, err)
	}

	settings := logconfig.Settings{
		Enabled: true, MinSeverity: logconfig.SeverityWarning, ExcludeUnits: []string{"cron.service"},
		Files: []logconfig.FileGlob{{Pattern: "/var/log/nginx/*.log", MultilineStart: `^\d`}},
	}
	if err := store.PutGroup(ctx, scopeFor(t, orgA), tenant.GroupID(group), &settings, alice, access.AuditEntry{Action: "log_settings.update", Target: "group:x"}); err != nil {
		t.Fatal(err)
	}
	rules := []logconfig.Rule{{Pattern: `customer-\d{6}`}}
	if err := store.PutRules(ctx, scopeFor(t, orgA), rules, alice, access.AuditEntry{Action: "redaction_rules.update", Target: "org:x"}); err != nil {
		t.Fatal(err)
	}
	config, err = store.ForNode(ctx, tenant.OrgID(orgA), identity.NodeID(node))
	if err != nil {
		t.Fatal(err)
	}
	if config.Version != start+2 || config.Settings.MinSeverity != logconfig.SeverityWarning || len(config.Settings.Files) != 1 ||
		config.Settings.Files[0].MultilineStart != `^\d` || len(config.Rules) != 1 || config.Rules[0] != rules[0] {
		t.Errorf("ForNode() = %+v, want the group's settings and the rules at version %d", config, start+2)
	}
	view, err := store.View(ctx, scopeFor(t, orgA))
	if err != nil || view.Version != start+2 || len(view.Rules) != 1 {
		t.Fatalf("View() = %+v, %v", view, err)
	}
	var custom int
	for _, g := range view.Groups {
		if g.Custom {
			custom++
			if g.ID != tenant.GroupID(group) || g.Settings.ExcludeUnits[0] != "cron.service" {
				t.Errorf("custom group = %+v", g)
			}
		} else if !g.Settings.Enabled {
			t.Errorf("group without settings = %+v, want the defaults", g)
		}
	}
	if custom != 1 {
		t.Errorf("%d groups with own settings, want 1", custom)
	}
	var audits int
	queryOwner(t, f.db.OwnerDSN, `SELECT count(*) FROM audit_log WHERE org_id = $1 AND action IN ('log_settings.update', 'redaction_rules.update')`, []any{orgA}, &audits)
	if audits < 2 {
		t.Errorf("%d audit entries, want one per change", audits)
	}

	// Back to the defaults.
	if err := store.PutGroup(ctx, scopeFor(t, orgA), tenant.GroupID(group), nil, alice, access.AuditEntry{Action: "log_settings.reset", Target: "group:x"}); err != nil {
		t.Fatal(err)
	}
	if config, _ := store.ForNode(ctx, tenant.OrgID(orgA), identity.NodeID(node)); config.Version != start+3 || config.Settings.MinSeverity != 0 || len(config.Rules) != 1 {
		t.Errorf("after the reset: %+v", config)
	}

	// The agent's answer.
	at := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	if err := store.Applied(ctx, tenant.OrgID(orgA), identity.NodeID(node), logconfig.Applied{Version: 7, Error: "rule 1: look-around is not supported", LogsVetoed: true, At: at}); err != nil {
		t.Fatal(err)
	}
	var applied int64
	var reason string
	var vetoed bool
	var reported time.Time
	queryOwner(t, f.db.OwnerDSN, `SELECT config_version, config_error, logs_vetoed, config_reported_at FROM nodes WHERE id = $1`, []any{node}, &applied, &reason, &vetoed, &reported)
	if applied != 7 || reason != "rule 1: look-around is not supported" || !vetoed || !reported.Equal(at) {
		t.Errorf("stored answer = %d %q %v %v", applied, reason, vetoed, reported)
	}
	// The fleet shows the veto (log explorer, M3-11).
	nodes, err := postgres.NewFleetStore(open(t, f.appDSN)).List(ctx, scopeFor(t, orgA))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		if uuid.UUID(n.ID) == node && !n.LogsVetoed {
			t.Errorf("node %v without the veto", n.ID)
		}
	}
}

func TestLogSettingsStore_KeepsOrganizationsApart(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	store := postgres.NewLogSettingsStore(open(t, f.appDSN))
	node := uuid.New()
	createNode(t, f.db.OwnerDSN, orgA, node)
	var group uuid.UUID
	queryOwner(t, f.db.OwnerDSN, `SELECT group_id FROM nodes WHERE id = $1`, []any{node}, &group)
	before, err := store.ForNode(ctx, tenant.OrgID(orgA), identity.NodeID(node))
	if err != nil {
		t.Fatal(err)
	}

	settings := logconfig.Settings{}
	if err := store.PutGroup(ctx, scopeFor(t, orgB), tenant.GroupID(group), &settings, identity.UserID{}, access.AuditEntry{Action: "log_settings.update", Target: "group:x"}); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("org B changed org A's group: %v", err)
	}
	if _, err := store.ForNode(ctx, tenant.OrgID(orgB), identity.NodeID(node)); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("org B read the configuration of org A's node: %v", err)
	}
	if err := store.PutRules(ctx, scopeFor(t, orgB), []logconfig.Rule{{Pattern: "b"}}, identity.UserID{}, access.AuditEntry{Action: "redaction_rules.update", Target: "org:b"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Applied(ctx, tenant.OrgID(orgB), identity.NodeID(node), logconfig.Applied{Version: 99, At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	after, err := store.ForNode(ctx, tenant.OrgID(orgA), identity.NodeID(node))
	if err != nil || after.Version != before.Version || len(after.Rules) != len(before.Rules) || !after.Settings.Enabled {
		t.Errorf("org A after org B's changes = %+v, %v; before %+v", after, err, before)
	}
	var applied int64
	queryOwner(t, f.db.OwnerDSN, `SELECT config_version FROM nodes WHERE id = $1`, []any{node}, &applied)
	if applied == 99 {
		t.Error("org B recorded an answer for org A's node")
	}
	view, err := store.View(ctx, scopeFor(t, orgB))
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range view.Groups {
		if g.ID == tenant.GroupID(group) {
			t.Errorf("org B sees org A's group %+v", g)
		}
	}
}
