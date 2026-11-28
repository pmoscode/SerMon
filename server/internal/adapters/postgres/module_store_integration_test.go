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

// M5-20: the fleet lists the modules a node runs – enabled for its group and built into its
// agent – for the rules of the modules.
func TestModuleStore_TheFleetKnowsTheModulesANodeRuns(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	db := open(t, f.appDSN)
	store, configs, fleetStore := postgres.NewModuleStore(db), postgres.NewLogSettingsStore(db), postgres.NewFleetStore(db)
	node := uuid.New()
	createNode(t, f.db.OwnerDSN, orgA, node)
	group := tenant.GroupID(groupOf(t, f.db.OwnerDSN, node))
	if _, err := configs.Announced(ctx, tenant.OrgID(orgA), identity.NodeID(node), []string{"nginx", "simulate"}); err != nil {
		t.Fatal(err)
	}
	audit := access.AuditEntry{Action: "module.update", Target: "g"}
	for _, s := range []module.Setting{{Module: "simulate", Enabled: true, Config: "{}"}, {Module: "nginx", Enabled: false, Config: "{}"}, {Module: "redis", Enabled: true, Config: "{}"}} {
		if err := store.Put(ctx, scopeFor(t, orgA), group, s, modules.SealedChange{}, identity.UserID{}, audit); err != nil {
			t.Fatal(err)
		}
	}
	nodes, err := fleetStore.List(ctx, scopeFor(t, orgA))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		if n.ID == identity.NodeID(node) && !slices.Equal(n.Modules, []string{"simulate"}) {
			t.Errorf("Modules = %v; want simulate only (nginx off, redis not built in)", n.Modules)
		}
	}
}

// V1-13: Check sees the credentials the module keeps, as stored in Put's transaction; its error
// ends the change. A removal waits for a change of the same module that holds its lock.
func TestModuleStore_ChecksInTheTransaction(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	store := postgres.NewModuleStore(open(t, f.appDSN))
	web := uuid.New()
	createNode(t, f.db.OwnerDSN, orgA, web)
	group := tenant.GroupID(groupOf(t, f.db.OwnerDSN, web))
	audit := access.AuditEntry{Action: "module.update", Target: "group:x"}
	setting := module.Setting{Module: "simulate", Enabled: true, Config: "{}"}
	if err := store.Put(ctx, scopeFor(t, orgA), group, setting, modules.SealedChange{Set: map[string][]byte{"token": []byte("sealed-1"), "other": []byte("sealed-2")}}, identity.UserID{}, audit); err != nil {
		t.Fatal(err)
	}
	var kept []string
	refuse := errors.New("refused")
	change := modules.SealedChange{
		Set: map[string][]byte{"third": []byte("sealed-3")}, Clear: []string{"other"},
		Check: func(k []string) (module.Setting, error) {
			kept = slices.Sorted(slices.Values(k))
			return module.Setting{}, refuse
		},
	}
	if err := store.Put(ctx, scopeFor(t, orgA), group, setting, change, identity.UserID{}, audit); !errors.Is(err, refuse) {
		t.Fatalf("Put() = %v", err)
	}
	if !slices.Equal(kept, []string{"third", "token"}) {
		t.Errorf("Check got %v", kept)
	}
	if fields, _ := store.SecretFields(ctx, scopeFor(t, orgA), group, "simulate"); !slices.Equal(fields, []string{"other", "token"}) {
		t.Errorf("after a refused change: %v", fields)
	}

	inCheck, release := make(chan struct{}), make(chan struct{})
	slow := modules.SealedChange{Check: func([]string) (module.Setting, error) {
		close(inCheck)
		<-release
		return module.Setting{Module: "simulate", Config: `{"value":2}`}, nil
	}}
	put := make(chan error, 1)
	go func() { put <- store.Put(ctx, scopeFor(t, orgA), group, setting, slow, identity.UserID{}, audit) }()
	<-inCheck
	removed := make(chan error, 1)
	go func() { removed <- store.Remove(ctx, scopeFor(t, orgA), group, "simulate", identity.UserID{}, audit) }()
	select {
	case err := <-removed:
		t.Fatalf("the removal didn't wait for the change: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	if err := <-put; err != nil {
		t.Fatal(err)
	}
	if err := <-removed; err != nil {
		t.Fatal(err)
	}
	if fields, _ := store.SecretFields(ctx, scopeFor(t, orgA), group, "simulate"); len(fields) != 0 {
		t.Errorf("after the removal: %v", fields)
	}
}

// V1-25: a suggestion dismissed for a group is gone for every node of the group – in the fleet
// and at the node – also after new reports of the agents, until shown again; other groups and
// modules keep theirs. Only a change is audited; another organization neither reads nor writes
// the dismissals, without a tenant nothing is visible.
func TestModuleStore_DismissedHints(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	db := open(t, f.appDSN)
	store, configs := postgres.NewModuleStore(db), postgres.NewLogSettingsStore(db)
	web1, web2, other := uuid.New(), uuid.New(), uuid.New()
	for _, n := range []uuid.UUID{web1, web2, other} {
		createNode(t, f.db.OwnerDSN, orgA, n)
	}
	group, otherGroup := groupOf(t, f.db.OwnerDSN, web1), groupOf(t, f.db.OwnerDSN, other)
	ownerExec(t, f.db.OwnerDSN, `UPDATE nodes SET group_id = $1 WHERE id = $2`, group, web2)
	at := time.Now().Truncate(time.Second)
	report := func(node uuid.UUID, hints ...module.Hint) {
		t.Helper()
		if _, err := configs.Announced(ctx, tenant.OrgID(orgA), identity.NodeID(node), []string{"nginx", "redis"}); err != nil {
			t.Fatal(err)
		}
		if err := store.WriteModuleHints(ctx, tenant.OrgID(orgA), identity.NodeID(node), module.Hints{CollectedAt: at, Hints: hints}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	nginx := module.Hint{Module: "nginx", Reasons: []string{"process:nginx"}}
	redis := module.Hint{Module: "redis", Reasons: []string{"port:6379"}}
	report(web1, nginx, redis)
	report(web2, nginx)
	report(other, nginx)
	open := func() map[uuid.UUID][]string {
		t.Helper()
		hints, err := store.OpenHints(ctx, scopeFor(t, orgA), 100)
		if err != nil {
			t.Fatal(err)
		}
		out := map[uuid.UUID][]string{}
		for _, h := range hints {
			out[uuid.UUID(h.Node)] = append(out[uuid.UUID(h.Node)], h.Hint.Module)
		}
		return out
	}
	atNode := func(node uuid.UUID) []string {
		t.Helper()
		n, err := store.Node(ctx, scopeFor(t, orgA), identity.NodeID(node))
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		if n.Hints != nil {
			for _, h := range n.Hints.Hints {
				out = append(out, h.Module)
			}
		}
		return out
	}
	audits := func(action string) int {
		t.Helper()
		var n int
		queryOwner(t, f.db.OwnerDSN, `SELECT count(*) FROM audit_log WHERE org_id = $1 AND action = $2 AND target = $3`, []any{orgA, action, "group:" + group.String()}, &n)
		return n
	}
	version := func() int64 {
		t.Helper()
		var v int64
		queryOwner(t, f.db.OwnerDSN, `SELECT agent_config_version FROM organizations WHERE id = $1`, []any{orgA}, &v)
		return v
	}
	before := version()
	dismiss := func(scope tenant.Scope, g uuid.UUID, mod string, dismissed bool) error {
		action := "module.hint_show"
		if dismissed {
			action = "module.hint_dismiss"
		}
		return store.SetHintDismissed(ctx, scope, tenant.GroupID(g), mod, dismissed, access.AuditEntry{Action: action, Target: "group:" + g.String()})
	}
	for range 2 {
		if err := dismiss(scopeFor(t, orgA), group, "nginx", true); err != nil {
			t.Fatal(err)
		}
	}
	got := open()
	if !slices.Equal(got[web1], []string{"redis"}) || len(got[web2]) != 0 || !slices.Equal(got[other], []string{"nginx"}) {
		t.Errorf("open hints %v: nginx dismissed for the group of web1 and web2 only", got)
	}
	if h := atNode(web1); !slices.Equal(h, []string{"redis"}) {
		t.Errorf("hints at web1 %v", h)
	}
	if h := atNode(other); !slices.Equal(h, []string{"nginx"}) {
		t.Errorf("hints at the other group's node %v", h)
	}
	if n := audits("module.hint_dismiss"); n != 1 {
		t.Errorf("%d audit entries for dismissing twice, want 1", n)
	}
	if v := version(); v != before {
		t.Errorf("version %d → %d: the agents get nothing new", before, v)
	}
	view, err := store.View(ctx, scopeFor(t, orgA))
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range view.Groups {
		switch uuid.UUID(g.ID) {
		case group:
			if !slices.Equal(g.Dismissed, []string{"nginx"}) {
				t.Errorf("View(group).Dismissed = %v", g.Dismissed)
			}
		case otherGroup:
			if len(g.Dismissed) != 0 {
				t.Errorf("View(other).Dismissed = %v", g.Dismissed)
			}
		}
	}
	// New reasons of the agent don't bring it back.
	report(web2, module.Hint{Module: "nginx", Reasons: []string{"process:nginx", "port:443", "unit:nginx.service"}})
	if got := open(); len(got[web2]) != 0 {
		t.Errorf("a new report brought it back: %v", got[web2])
	}

	// Another organization, an unknown group, no tenant.
	if err := dismiss(scopeFor(t, orgB), group, "redis", true); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("org B dismissed in org A's group: %v", err)
	}
	if err := dismiss(scopeFor(t, orgB), group, "nginx", false); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("org B showed in org A's group: %v", err)
	}
	if err := dismiss(scopeFor(t, orgA), uuid.New(), "nginx", true); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("unknown group: %v", err)
	}
	if view, _ := store.View(ctx, scopeFor(t, orgB)); slices.ContainsFunc(view.Groups, func(g modules.Group) bool { return len(g.Dismissed) > 0 }) {
		t.Errorf("org B sees %+v", view.Groups)
	}
	var rows int
	queryOwner(t, f.db.OwnerDSN, `SELECT count(*) FROM module_hint_dismissals WHERE group_id = $1`, []any{group}, &rows)
	if rows != 1 {
		t.Errorf("%d dismissals in org A's group", rows)
	}
	var enabled, forced bool
	queryOwner(t, f.db.OwnerDSN, "SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE relname = 'module_hint_dismissals'", nil, &enabled, &forced)
	if !enabled || !forced {
		t.Errorf("module_hint_dismissals: RLS %v %v", enabled, forced)
	}
	conn, err := pgx.Connect(ctx, f.appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var n int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM module_hint_dismissals").Scan(&n); err != nil || n != 0 {
		t.Errorf("without tenant: %d, %v", n, err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('sermon.org_id', $1, true)", orgB.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO module_hint_dismissals (org_id, group_id, module_id) VALUES ($1, $2, 'redis')`, orgA, group); err == nil {
		t.Error("a row for another organization must be rejected")
	}
	_ = tx.Rollback(ctx)

	// Shown again: back for both nodes; showing twice is audited once.
	for range 2 {
		if err := dismiss(scopeFor(t, orgA), group, "nginx", false); err != nil {
			t.Fatal(err)
		}
	}
	if got := open(); !slices.Equal(got[web1], []string{"nginx", "redis"}) || !slices.Equal(got[web2], []string{"nginx"}) {
		t.Errorf("after showing again: %v", got)
	}
	if h := atNode(web2); !slices.Equal(h, []string{"nginx"}) {
		t.Errorf("hints at web2 %v", h)
	}
	if n := audits("module.hint_show"); n != 1 {
		t.Errorf("%d audit entries for showing twice, want 1", n)
	}
}
