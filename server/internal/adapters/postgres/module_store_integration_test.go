//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/modules"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/logconfig"
	"github.com/pmoscode/SerMon/server/internal/domain/module"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var moduleTables = []string{"module_settings", "module_health", "module_hints"}

func groupOf(t *testing.T, ownerDSN string, node uuid.UUID) uuid.UUID {
	t.Helper()
	var group uuid.UUID
	queryOwner(t, ownerDSN, `SELECT group_id FROM nodes WHERE id = $1`, []any{node}, &group)
	return group
}

// M5-06: a change of a group's modules raises the version and reaches the nodes of that group
// only; the agent's built-in modules are stored and their change reported.
func TestModuleStore_AGroupsModulesReachItsNodesOnly(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	db := open(t, f.appDSN)
	store, configs := postgres.NewModuleStore(db), postgres.NewLogSettingsStore(db)
	web, other := uuid.New(), uuid.New()
	createNode(t, f.db.OwnerDSN, orgA, web)
	createNode(t, f.db.OwnerDSN, orgA, other)
	group := groupOf(t, f.db.OwnerDSN, web)
	before, err := configs.ForNode(ctx, tenant.OrgID(orgA), identity.NodeID(web))
	if err != nil || len(before.Modules) != 0 {
		t.Fatalf("ForNode() = %+v, %v", before, err)
	}
	alice := identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000a1"))
	setting := module.Setting{Module: "simulate", Enabled: true, Interval: 20 * time.Second, Config: `{"value":3}`}
	if err := store.Put(ctx, scopeFor(t, orgA), tenant.GroupID(group), setting, modules.SealedChange{}, alice, access.AuditEntry{Actor: alice, Action: "module.update", Target: "group:x"}); err != nil {
		t.Fatal(err)
	}
	got, err := configs.ForNode(ctx, tenant.OrgID(orgA), identity.NodeID(web))
	if err != nil || got.Version != before.Version+1 || len(got.Modules) != 1 || !reflect.DeepEqual(got.Modules[0], setting) {
		t.Fatalf("ForNode(web) = %+v, %v; want the module at version %d", got, err, before.Version+1)
	}
	if o, err := configs.ForNode(ctx, tenant.OrgID(orgA), identity.NodeID(other)); err != nil || len(o.Modules) != 0 || o.Version != got.Version {
		t.Errorf("ForNode(other group) = %+v, %v; want the version but no module", o, err)
	}
	setting.Enabled, setting.Interval = false, 0
	if err := store.Put(ctx, scopeFor(t, orgA), tenant.GroupID(group), setting, modules.SealedChange{}, alice, access.AuditEntry{Action: "module.update", Target: "group:x"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := configs.ForNode(ctx, tenant.OrgID(orgA), identity.NodeID(web)); len(got.Modules) != 1 || !reflect.DeepEqual(got.Modules[0], setting) {
		t.Errorf("updated: %+v", got.Modules)
	}
	var audits int
	queryOwner(t, f.db.OwnerDSN, `SELECT count(*) FROM audit_log WHERE org_id = $1 AND action = 'module.update'`, []any{orgA}, &audits)
	if audits < 2 {
		t.Errorf("%d audit entries", audits)
	}

	for i, tc := range []struct {
		modules []string
		changed bool
	}{{[]string{"simulate", "nginx"}, true}, {[]string{"nginx", "simulate"}, false}, {[]string{"nginx"}, true}} {
		changed, err := configs.Announced(ctx, tenant.OrgID(orgA), identity.NodeID(web), tc.modules)
		if err != nil || changed != tc.changed {
			t.Errorf("Announced #%d = %v, %v; want %v", i, changed, err, tc.changed)
		}
	}
	// A list stored in another order (an older server) is the same set.
	ownerExec(t, f.db.OwnerDSN, `UPDATE nodes SET modules = '{simulate,nginx}' WHERE id = $1`, other)
	if changed, err := configs.Announced(ctx, tenant.OrgID(orgA), identity.NodeID(other), []string{"nginx", "simulate"}); err != nil || changed {
		t.Errorf("same set in another order: %v, %v", changed, err)
	}
	n, err := store.Node(ctx, scopeFor(t, orgA), identity.NodeID(web))
	if err != nil || !slices.Equal(n.Builtin, []string{"nginx"}) || n.Group != tenant.GroupID(group) || len(n.Settings) != 1 {
		t.Errorf("Node() = %+v, %v", n, err)
	}
	if _, err := configs.Announced(ctx, tenant.OrgID(orgB), identity.NodeID(web), []string{"redis"}); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("org B announced for org A's node: %v", err)
	}
	if err := configs.Applied(ctx, tenant.OrgID(orgA), identity.NodeID(web), logconfig.Applied{Version: got.Version, ModulesVetoed: true, At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if n, _ := store.Node(ctx, scopeFor(t, orgA), identity.NodeID(web)); !n.Vetoed {
		t.Error("the modules' veto was not stored")
	}

	if err := store.Remove(ctx, scopeFor(t, orgA), tenant.GroupID(group), "simulate", alice, access.AuditEntry{Action: "module.remove", Target: "group:x"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(ctx, scopeFor(t, orgA), tenant.GroupID(group), "simulate", alice, access.AuditEntry{Action: "module.remove", Target: "group:x"}); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("removed twice: %v", err)
	}
}

func TestModuleStore_AGroupHasAtMost32Modules(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	store := postgres.NewModuleStore(open(t, f.appDSN))
	node := uuid.New()
	createNode(t, f.db.OwnerDSN, orgA, node)
	group := tenant.GroupID(groupOf(t, f.db.OwnerDSN, node))
	audit := access.AuditEntry{Action: "module.update", Target: "group:x"}
	for i := range module.MaxModules {
		if err := store.Put(ctx, scopeFor(t, orgA), group, module.Setting{Module: "m" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Config: "{}"}, modules.SealedChange{}, identity.UserID{}, audit); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Put(ctx, scopeFor(t, orgA), group, module.Setting{Module: "one_more", Config: "{}"}, modules.SealedChange{}, identity.UserID{}, audit); !errors.Is(err, module.ErrInvalid) {
		t.Errorf("33rd module: %v", err)
	}
	if err := store.Put(ctx, scopeFor(t, orgA), group, module.Setting{Module: "maa", Enabled: true, Config: "{}"}, modules.SealedChange{}, identity.UserID{}, audit); err != nil {
		t.Errorf("changing one of the 32: %v", err)
	}
}

// The agent's reports replace the node's previous ones; open hints leave out modules the group
// runs and modules the agent doesn't have built in.
func TestModuleStore_ReportsOfTheAgents(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	db := open(t, f.appDSN)
	store, configs := postgres.NewModuleStore(db), postgres.NewLogSettingsStore(db)
	node := uuid.New()
	createNode(t, f.db.OwnerDSN, orgA, node)
	group := tenant.GroupID(groupOf(t, f.db.OwnerDSN, node))
	if _, err := configs.Announced(ctx, tenant.OrgID(orgA), identity.NodeID(node), []string{"nginx", "simulate"}); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Truncate(time.Second)
	success := at.Add(-time.Minute)
	first := module.HealthReport{CollectedAt: at.Add(-time.Hour), Modules: []module.Health{{Module: "nginx", State: module.StateOK}, {Module: "simulate", State: module.StateOK}}}
	newest := module.HealthReport{CollectedAt: at, Modules: []module.Health{{Module: "simulate", State: module.StateError, Reason: "simulated failure", LastSuccess: &success}}}
	for _, r := range []module.HealthReport{first, newest} {
		if err := store.WriteModuleHealth(ctx, tenant.OrgID(orgA), identity.NodeID(node), r, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	hints := module.Hints{CollectedAt: at, Hints: []module.Hint{
		{Module: "nginx", Reasons: []string{"process:nginx", "port:80"}, Suggested: `{"port":80}`},
		{Module: "simulate", Reasons: []string{"process:simulate"}},
		{Module: "redis", Reasons: []string{"port:6379"}},
	}}
	if err := store.WriteModuleHints(ctx, tenant.OrgID(orgA), identity.NodeID(node), hints, time.Now()); err != nil {
		t.Fatal(err)
	}
	n, err := store.Node(ctx, scopeFor(t, orgA), identity.NodeID(node))
	if err != nil || n.Health == nil || len(n.Health.Modules) != 1 || n.Health.Modules[0].Reason != "simulated failure" ||
		!n.Health.Modules[0].LastSuccess.Equal(success) || !n.Health.CollectedAt.Equal(at) {
		t.Fatalf("Node().Health = %+v, %v", n.Health, err)
	}
	if n.Hints == nil || len(n.Hints.Hints) != 3 || n.Hints.Hints[0].Suggested != `{"port":80}` {
		t.Fatalf("Node().Hints = %+v", n.Hints)
	}
	if err := store.Put(ctx, scopeFor(t, orgA), group, module.Setting{Module: "simulate", Enabled: true, Config: "{}"}, modules.SealedChange{}, identity.UserID{}, access.AuditEntry{Action: "module.update", Target: "g"}); err != nil {
		t.Fatal(err)
	}
	pending, err := store.OpenHints(ctx, scopeFor(t, orgA), 100)
	if err != nil {
		t.Fatal(err)
	}
	var mine []string
	for _, h := range pending {
		if h.Node == identity.NodeID(node) {
			mine = append(mine, h.Hint.Module)
		}
	}
	if !slices.Equal(mine, []string{"nginx"}) {
		t.Errorf("open hints %v: want nginx (simulate runs, redis is not built in)", mine)
	}
	other, _ := tenant.ForGroups(tenant.OrgID(orgA), tenant.GroupID(uuid.New()))
	if hints, _ := store.OpenHints(ctx, other, 100); len(hints) != 0 {
		t.Errorf("another group's scope sees %+v", hints)
	}
	if _, err := store.Node(ctx, other, identity.NodeID(node)); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("another group's scope reads the node: %v", err)
	}
	if err := store.WriteModuleHealth(ctx, tenant.OrgID(orgA), identity.NodeID(node), module.HealthReport{CollectedAt: at}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if n, _ := store.Node(ctx, scopeFor(t, orgA), identity.NodeID(node)); n.Health != nil {
		t.Errorf("an empty report keeps %+v", n.Health)
	}
}

// RLS: the tables are forced, another organization neither reads nor writes, without a tenant
// nothing is visible.
func TestModuleStore_KeepsOrganizationsApart(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	store := postgres.NewModuleStore(open(t, f.appDSN))
	node := uuid.New()
	createNode(t, f.db.OwnerDSN, orgA, node)
	group := tenant.GroupID(groupOf(t, f.db.OwnerDSN, node))
	audit := access.AuditEntry{Action: "module.update", Target: "group:x"}
	if err := store.Put(ctx, scopeFor(t, orgA), group, module.Setting{Module: "simulate", Config: `{"value":1}`}, modules.SealedChange{}, identity.UserID{}, audit); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteModuleHints(ctx, tenant.OrgID(orgA), identity.NodeID(node), module.Hints{CollectedAt: time.Now(), Hints: []module.Hint{{Module: "nginx", Reasons: []string{"port:80"}}}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, table := range moduleTables {
		var enabled, forced bool
		queryOwner(t, f.db.OwnerDSN, "SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE relname = $1", []any{table}, &enabled, &forced)
		if !enabled || !forced {
			t.Errorf("%s: RLS enabled %v, forced %v", table, enabled, forced)
		}
	}

	b := scopeFor(t, orgB)
	if err := store.Put(ctx, b, group, module.Setting{Module: "simulate", Config: "{}"}, modules.SealedChange{}, identity.UserID{}, audit); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("org B set org A's group: %v", err)
	}
	if err := store.Remove(ctx, b, group, "simulate", identity.UserID{}, audit); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("org B removed org A's module: %v", err)
	}
	if _, err := store.Node(ctx, b, identity.NodeID(node)); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("org B read org A's node: %v", err)
	}
	view, err := store.View(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range view.Groups {
		if g.ID == group || len(g.Modules) > 0 && g.Modules[0].Config == `{"value":1}` {
			t.Errorf("org B sees %+v", g)
		}
	}
	if hints, _ := store.OpenHints(ctx, b, 100); slices.ContainsFunc(hints, func(h modules.NodeHint) bool { return h.Node == identity.NodeID(node) }) {
		t.Error("org B sees org A's hints")
	}
	// The agent of org B can't write reports for org A's node: the row would not be org A's.
	err = store.WriteModuleHealth(ctx, tenant.OrgID(orgB), identity.NodeID(node), module.HealthReport{CollectedAt: time.Now(), Modules: []module.Health{{Module: "simulate", State: module.StateOK}}}, time.Now())
	if err == nil {
		t.Error("org B wrote health for org A's node")
	}
	if err := store.WriteModuleHints(ctx, tenant.OrgID(orgB), identity.NodeID(node), module.Hints{CollectedAt: time.Now()}, time.Now()); err != nil {
		t.Fatal(err)
	}
	var hintsLeft int
	queryOwner(t, f.db.OwnerDSN, "SELECT count(*) FROM module_hints WHERE node_id = $1", []any{node}, &hintsLeft)
	if hintsLeft != 1 {
		t.Errorf("org B's empty report removed org A's hints: %d left", hintsLeft)
	}

	conn, err := pgx.Connect(ctx, f.appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	for _, table := range moduleTables {
		var n int
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s without tenant: %d rows, %v", table, n, err)
		}
	}
}

// M5-05: the sealed credentials of a group's module are stored, removed, removed with their
// setting, and given only to the nodes of that group; the views name the fields, never a value;
// another organization neither reads nor writes them.
func TestModuleStore_Credentials(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	db := open(t, f.appDSN)
	store, configs := postgres.NewModuleStore(db), postgres.NewLogSettingsStore(db)
	web, other := uuid.New(), uuid.New()
	createNode(t, f.db.OwnerDSN, orgA, web)
	createNode(t, f.db.OwnerDSN, orgA, other)
	group := tenant.GroupID(groupOf(t, f.db.OwnerDSN, web))
	audit := access.AuditEntry{Action: "module.update", Target: "group:x"}
	setting := module.Setting{Module: "simulate", Enabled: true, Config: "{}"}
	if err := store.Put(ctx, scopeFor(t, orgA), group, setting, modules.SealedChange{Set: map[string][]byte{"token": []byte("sealed-1"), "other": []byte("sealed-2")}}, identity.UserID{}, audit); err != nil {
		t.Fatal(err)
	}
	fields, err := store.SecretFields(ctx, scopeFor(t, orgA), group, "simulate")
	if err != nil || !slices.Equal(fields, []string{"other", "token"}) {
		t.Fatalf("SecretFields() = %v, %v", fields, err)
	}
	got, err := configs.ForNode(ctx, tenant.OrgID(orgA), identity.NodeID(web))
	if err != nil || len(got.Secrets) != 2 || got.Secrets[1].Field != "token" || string(got.Secrets[1].Sealed) != "sealed-1" ||
		got.Secrets[1].Group != group || got.Secrets[1].Module != "simulate" {
		t.Fatalf("ForNode().Secrets = %+v, %v", got.Secrets, err)
	}
	if o, _ := configs.ForNode(ctx, tenant.OrgID(orgA), identity.NodeID(other)); len(o.Secrets) != 0 {
		t.Errorf("a node of another group gets %+v", o.Secrets)
	}
	view, _ := store.View(ctx, scopeFor(t, orgA))
	for _, g := range view.Groups {
		if g.ID == group && (len(g.Modules) != 1 || !slices.Equal(g.Modules[0].SecretsSet, []string{"other", "token"})) {
			t.Errorf("View() = %+v", g)
		}
	}
	if n, _ := store.Node(ctx, scopeFor(t, orgA), identity.NodeID(web)); len(n.Settings) != 1 || len(n.Settings[0].SecretsSet) != 2 {
		t.Errorf("Node() = %+v", n.Settings)
	}

	// Org B: no write, no read.
	if err := store.Put(ctx, scopeFor(t, orgB), group, setting, modules.SealedChange{Set: map[string][]byte{"token": []byte("b")}}, identity.UserID{}, audit); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("org B: %v", err)
	}
	if fields, _ := store.SecretFields(ctx, scopeFor(t, orgB), group, "simulate"); len(fields) != 0 {
		t.Errorf("org B reads %v", fields)
	}
	if _, err := configs.ForNode(ctx, tenant.OrgID(orgB), identity.NodeID(web)); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("org B: %v", err)
	}

	if err := store.Put(ctx, scopeFor(t, orgA), group, setting, modules.SealedChange{Set: map[string][]byte{"token": []byte("sealed-3")}, Clear: []string{"other"}}, identity.UserID{}, audit); err != nil {
		t.Fatal(err)
	}
	if got, _ := configs.ForNode(ctx, tenant.OrgID(orgA), identity.NodeID(web)); len(got.Secrets) != 1 || string(got.Secrets[0].Sealed) != "sealed-3" {
		t.Errorf("after the change: %+v", got.Secrets)
	}
	if err := store.Remove(ctx, scopeFor(t, orgA), group, "simulate", identity.UserID{}, audit); err != nil {
		t.Fatal(err)
	}
	var left int
	queryOwner(t, f.db.OwnerDSN, "SELECT count(*) FROM module_secrets WHERE group_id = $1", []any{uuid.UUID(group)}, &left)
	if left != 0 {
		t.Errorf("%d credentials outlived their setting", left)
	}
	if err := store.Put(ctx, scopeFor(t, orgA), group, setting, modules.SealedChange{Set: map[string][]byte{"token": []byte("sealed-4")}}, identity.UserID{}, audit); err != nil {
		t.Fatal(err)
	}
	var enabled, forced bool
	queryOwner(t, f.db.OwnerDSN, "SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE relname = 'module_secrets'", nil, &enabled, &forced)
	if !enabled || !forced {
		t.Errorf("module_secrets: RLS %v %v", enabled, forced)
	}
	conn, err := pgx.Connect(ctx, f.appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var n int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM module_secrets").Scan(&n); err != nil || n != 0 {
		t.Errorf("without tenant: %d, %v", n, err)
	}
}
