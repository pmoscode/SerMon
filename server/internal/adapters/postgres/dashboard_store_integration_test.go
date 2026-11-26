//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/dashboards"
	"github.com/pmoscode/SerMon/server/internal/domain/dashboard"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	"github.com/pmoscode/SerMon/server/internal/platform/ratelimit"
)

type dashboardEnv struct {
	f     fixture
	store *postgres.DashboardStore
	svc   *dashboards.Service
}

func newDashboardEnv(t *testing.T) dashboardEnv {
	t.Helper()
	f := setup(t)
	store := postgres.NewDashboardStore(open(t, f.appDSN))
	return dashboardEnv{f: f, store: store, svc: dashboards.NewService(store, authz.NewPolicy(), ratelimit.New(1000, time.Minute, time.Now), time.Now, uuid.New)}
}

func dashboardNode(t *testing.T, f fixture, org tenant.OrgID, group tenant.GroupID) identity.NodeID {
	t.Helper()
	id := uuid.New()
	ownerExec(t, f.db.OwnerDSN, `INSERT INTO nodes (id, org_id, group_id, hostname, machine_id, os_id, os_version, kernel_release, architecture, cpu_count, memory_bytes)
		VALUES ($1, $2, $3, 'host', $4, 'debian', '13', '6.12.0', 'amd64', 2, 1073741824)`,
		id, uuid.UUID(org), uuid.UUID(group), strings.ReplaceAll(id.String(), "-", ""))
	return identity.NodeID(id)
}

func chartOf(nodes ...identity.NodeID) dashboard.Spec {
	return dashboard.Spec{Version: 1, Title: "Web", Widgets: []dashboard.Widget{{
		ID: "cpu", Kind: dashboard.KindTimeseries, Title: "CPU", Position: dashboard.Position{W: 6, H: 4},
		Query: dashboard.Query{Metric: "sermon_cpu_utilization_ratio", Labels: map[string]string{"mode": "user"}, Target: dashboard.Target{Nodes: nodes}},
	}}}
}

func member(o mgmtOrg, user identity.UserID, bindings ...authz.Binding) access.Caller {
	return access.Caller{Principal: authz.Principal{User: user, Org: o.scope.OrgID(), Bindings: bindings}}
}

var allAdmin = authz.Binding{AllGroups: true, Role: role.Admin}

func TestDashboardStore_RoundTripPrivateAndShared(t *testing.T) {
	e := newDashboardEnv(t)
	ctx := context.Background()
	o := newMgmtOrg(t, e.f)
	group := newGroup(t, e.f, o.scope.OrgID())
	node := dashboardNode(t, e.f, o.scope.OrgID(), group)
	admin := member(o, o.admin, allAdmin)
	viewer := member(o, o.other, authz.Binding{Group: group, Role: role.Viewer})

	s := chartOf(node)
	s.Variables = []dashboard.Variable{{Name: "group", Kind: dashboard.VariableGroup, Default: uuid.UUID(group)}}
	d, err := e.svc.Create(ctx, admin, s)
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.Get(ctx, admin, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.OwnerName != "User" || got.Revision != 1 || got.Spec.Widgets[0].Query.Labels["mode"] != "user" ||
		got.Spec.Variables[0].Default != uuid.UUID(group) || got.Spec.Widgets[0].Query.Target.Nodes[0] != node {
		t.Errorf("round trip = %+v", got)
	}
	if _, err := e.svc.Get(ctx, viewer, d.ID); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("private of another: %v", err)
	}
	if list, _ := e.svc.List(ctx, viewer); len(list) != 0 {
		t.Errorf("list of the other = %+v", list)
	}
	if _, err := e.svc.SetShared(ctx, admin, d.ID, true); err != nil {
		t.Fatal(err)
	}
	if list, err := e.svc.List(ctx, viewer); err != nil || len(list) != 1 || list[0].CanEdit {
		t.Errorf("shared for the other = %+v, %v", list, err)
	}
	if _, err := e.svc.Update(ctx, viewer, d.ID, 2, chartOf(node)); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("viewer changes a shared one: %v", err)
	}
	if _, err := e.svc.Update(ctx, admin, d.ID, 1, chartOf(node)); !errors.Is(err, dashboards.ErrConflict) {
		t.Errorf("stale revision: %v", err)
	}
	updated, err := e.svc.Update(ctx, admin, d.ID, 2, chartOf(node))
	if err != nil || updated.Revision != 3 {
		t.Fatalf("Update() = %+v, %v", updated, err)
	}
	if err := e.svc.Delete(ctx, admin, d.ID); err != nil {
		t.Fatal(err)
	}
	var audits []string
	ownerQueryRow(t, e.f.db.OwnerDSN, `SELECT array_agg(action ORDER BY seq) FROM audit_log WHERE org_id = $1 AND target = $2`,
		[]any{uuid.UUID(o.scope.OrgID()), "dashboard:" + d.ID.String()}, &audits)
	if strings.Join(audits, ",") != "dashboard.create,dashboard.share,dashboard.update,dashboard.delete" {
		t.Errorf("audit = %v", audits)
	}
}

// Saving checks nodes and groups against what the caller may view: another group, another
// organization, nobody's.
func TestDashboardStore_RejectsNodesAndGroupsTheCallerCannotView(t *testing.T) {
	e := newDashboardEnv(t)
	ctx := context.Background()
	o, other := newMgmtOrg(t, e.f), newMgmtOrg(t, e.f)
	mine, theirs := newGroup(t, e.f, o.scope.OrgID()), newGroup(t, e.f, o.scope.OrgID())
	foreignGroup := newGroup(t, e.f, other.scope.OrgID())
	inMine := dashboardNode(t, e.f, o.scope.OrgID(), mine)
	inTheirs := dashboardNode(t, e.f, o.scope.OrgID(), theirs)
	foreign := dashboardNode(t, e.f, other.scope.OrgID(), foreignGroup)
	viewer := member(o, o.other, authz.Binding{Group: mine, Role: role.Viewer})

	if _, err := e.svc.Create(ctx, viewer, chartOf(inMine)); err != nil {
		t.Fatalf("own group: %v", err)
	}
	for name, s := range map[string]dashboard.Spec{
		"node of another group":         chartOf(inTheirs),
		"node of another organization":  chartOf(foreign),
		"node of nobody":                chartOf(identity.NodeID(uuid.New())),
		"group of another organization": func() dashboard.Spec { s := chartOf(); s.Widgets[0].Query.Target.Group = &foreignGroup; return s }(),
	} {
		if _, err := e.svc.Create(ctx, viewer, s); !errors.Is(err, dashboards.ErrUnknownReference) {
			t.Errorf("%s: %v, want ErrUnknownReference", name, err)
		}
	}
	admin := member(o, o.admin, allAdmin)
	if _, err := e.svc.Create(ctx, admin, chartOf(foreign)); !errors.Is(err, dashboards.ErrUnknownReference) {
		t.Errorf("an admin of all groups saves a node of another organization: %v", err)
	}
}

func TestDashboardStore_TenantIsolation(t *testing.T) {
	e := newDashboardEnv(t)
	ctx := context.Background()
	a, b := newMgmtOrg(t, e.f), newMgmtOrg(t, e.f)
	d, err := e.svc.Create(ctx, member(a, a.admin, allAdmin), chartOf())
	if err == nil {
		t.Fatal("a chart without target must be rejected")
	}
	alerts := dashboard.Spec{Version: 1, Title: "Alerts", Widgets: []dashboard.Widget{{
		ID: "alerts", Kind: dashboard.KindAlertList, Title: "Alerts", Position: dashboard.Position{W: 4, H: 3},
	}}}
	d, err = e.svc.Create(ctx, member(a, a.admin, allAdmin), alerts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetShared(ctx, member(a, a.admin, allAdmin), d.ID, true); err != nil {
		t.Fatal(err)
	}
	// The same user ID in another organization sees nothing of it, shared or not.
	intruder := member(b, a.admin, allAdmin)
	if _, err := e.svc.Get(ctx, intruder, d.ID); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("Get from another organization: %v", err)
	}
	if list, _ := e.svc.List(ctx, intruder); len(list) != 0 {
		t.Errorf("List from another organization = %v", list)
	}
	if _, err := e.svc.Update(ctx, intruder, d.ID, 2, alerts); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("Update from another organization: %v", err)
	}
	if err := e.svc.Delete(ctx, intruder, d.ID); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("Delete from another organization: %v", err)
	}

	// RLS: without a tenant nothing is visible, and no row of another organization can be written.
	conn, err := pgx.Connect(ctx, e.f.appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var n int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM dashboards").Scan(&n); err != nil || n != 0 {
		t.Errorf("without tenant: %d rows, %v", n, err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT set_config('sermon.org_id', $1, true)", b.scope.OrgID().String()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO dashboards (org_id, id, owner_id, title, spec, created_at, updated_at)
		VALUES ($1, $2, $3, 'x', '{}', now(), now())`, uuid.UUID(a.scope.OrgID()), uuid.New(), uuid.UUID(a.admin)); err == nil {
		t.Error("a row for another organization must be rejected")
	}
}

func TestDashboardStore_IsForced(t *testing.T) {
	f := setup(t)
	var enabled, forced bool
	queryOwner(t, f.db.OwnerDSN, "SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE relname = 'dashboards'", nil, &enabled, &forced)
	if !enabled || !forced {
		t.Errorf("dashboards: RLS enabled %v, forced %v", enabled, forced)
	}
}

func TestDashboardStore_LimitOfTheOrganization(t *testing.T) {
	e := newDashboardEnv(t)
	ctx := context.Background()
	o := newMgmtOrg(t, e.f)
	ownerExec(t, e.f.db.OwnerDSN, `INSERT INTO dashboards (org_id, id, owner_id, title, spec, created_at, updated_at)
		SELECT $1, gen_random_uuid(), $2, 'd' || i, '{"version":1,"title":"x"}', now(), now() FROM generate_series(1, $3::int) i`,
		uuid.UUID(o.scope.OrgID()), uuid.UUID(o.admin), dashboards.MaxPerOrganization)
	alerts := dashboard.Spec{Version: 1, Title: "One more"}
	if _, err := e.svc.Create(ctx, member(o, o.admin, allAdmin), alerts); !errors.Is(err, dashboards.ErrTooMany) {
		t.Errorf("Create() = %v, want ErrTooMany", err)
	}
	other := newMgmtOrg(t, e.f)
	if _, err := e.svc.Create(ctx, member(other, other.admin, allAdmin), alerts); err != nil {
		t.Errorf("another organization is not limited by it: %v", err)
	}
}

// M5-33: a document Validate takes fits the table's check, also at MaxDocumentBytes with a blank
// in the stored text after every ':' and ','.
func TestDashboardStore_KeepsTheLargestValidDocument(t *testing.T) {
	e := newDashboardEnv(t)
	o := newMgmtOrg(t, e.f)
	nodes := make([]identity.NodeID, dashboard.MaxNodes)
	for i := range nodes {
		nodes[i] = identity.NodeID(uuid.New())
	}
	wide := strings.Repeat("𝄞", dashboard.MaxTitle)
	spec := dashboard.Spec{Version: 1, Title: wide, Description: strings.Repeat("𝄞", dashboard.MaxDescription), TimeRange: dashboard.Preset1h}
	for i := range dashboard.MaxWidgets {
		spec.Widgets = append(spec.Widgets, dashboard.Widget{
			ID: fmt.Sprintf("w%031d", i), Kind: dashboard.KindLogStream, Title: "x",
			Position: dashboard.Position{X: i % dashboard.Columns, Y: i / dashboard.Columns, W: 1, H: 1},
			Query:    dashboard.Query{Target: dashboard.Target{Nodes: nodes}, Text: "a"},
		})
	}
	// Grow the titles until the document is at the limit.
	for i := range spec.Widgets {
		for {
			spec.Widgets[i].Title += "𝄞"
			raw, _ := json.Marshal(spec)
			if len(raw) > dashboard.MaxDocumentBytes || len([]rune(spec.Widgets[i].Title)) > dashboard.MaxTitle {
				spec.Widgets[i].Title = string([]rune(spec.Widgets[i].Title)[:len([]rune(spec.Widgets[i].Title))-1])
				break
			}
		}
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	raw, _ := json.Marshal(spec)
	if len(raw) < dashboard.MaxDocumentBytes-1024 {
		t.Fatalf("fixture of %d bytes: want it at the limit", len(raw))
	}
	d := dashboards.Dashboard{ID: dashboards.ID(uuid.New()), Owner: o.admin, Revision: 1, Spec: spec, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := e.store.Create(context.Background(), o.scope, d, access.AuditEntry{Action: "dashboard.create", Target: "dashboard:" + d.ID.String()}); err != nil {
		t.Errorf("Create of a valid document of %d bytes: %v", len(raw), err)
	}
}
