package dashboards_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/dashboards"
	"github.com/pmoscode/SerMon/server/internal/domain/dashboard"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	orgA   = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
	groupX = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000aa"))
	groupY = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000bb"))
	alice  = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001"))
	bob    = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000002"))
	carol  = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000003"))
	nodeX  = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000a1")) // in group X
	nodeY  = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000b1")) // in group Y
)

// fakeStore keeps dashboards in memory and behaves like the PostgreSQL store: List and Get return
// the caller's and the shared ones, Change and Delete lock a visible one.
type fakeStore struct {
	mu     sync.Mutex
	byID   map[dashboards.ID]dashboards.Dashboard
	audits []access.AuditEntry
	// nodes and groups are the organization's, with the group of each node.
	nodes map[identity.NodeID]tenant.GroupID
}

func newStore() *fakeStore {
	return &fakeStore{
		byID:  map[dashboards.ID]dashboards.Dashboard{},
		nodes: map[identity.NodeID]tenant.GroupID{nodeX: groupX, nodeY: groupY},
	}
}

func (f *fakeStore) visible(d dashboards.Dashboard, user identity.UserID) bool {
	return d.Owner == user || d.Shared
}

func (f *fakeStore) List(_ context.Context, scope tenant.Scope, user identity.UserID) ([]dashboards.Dashboard, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []dashboards.Dashboard
	for _, d := range f.byID {
		if f.visible(d, user) && scope.OrgID() == orgA {
			out = append(out, d)
		}
	}
	slices.SortFunc(out, func(a, b dashboards.Dashboard) int { return strings.Compare(a.Spec.Title, b.Spec.Title) })
	return out, nil
}

func (f *fakeStore) Get(_ context.Context, _ tenant.Scope, user identity.UserID, id dashboards.ID) (dashboards.Dashboard, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.byID[id]
	if !ok || !f.visible(d, user) {
		return dashboards.Dashboard{}, access.ErrNotFound
	}
	return d, nil
}

func (f *fakeStore) Create(_ context.Context, _ tenant.Scope, d dashboards.Dashboard, audit access.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.byID) >= dashboards.MaxPerOrganization {
		return dashboards.ErrTooMany
	}
	f.byID[d.ID] = d
	f.audits = append(f.audits, audit)
	return nil
}

func (f *fakeStore) Change(_ context.Context, _ tenant.Scope, user identity.UserID, id dashboards.ID,
	change func(dashboards.Dashboard) (dashboards.Dashboard, access.AuditEntry, error),
) (dashboards.Dashboard, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.byID[id]
	if !ok || !f.visible(d, user) {
		return dashboards.Dashboard{}, access.ErrNotFound
	}
	next, audit, err := change(d)
	if err != nil {
		return dashboards.Dashboard{}, err
	}
	f.byID[id] = next
	f.audits = append(f.audits, audit)
	return next, nil
}

func (f *fakeStore) Delete(_ context.Context, _ tenant.Scope, user identity.UserID, id dashboards.ID,
	check func(dashboards.Dashboard) (access.AuditEntry, error),
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.byID[id]
	if !ok || !f.visible(d, user) {
		return access.ErrNotFound
	}
	audit, err := check(d)
	if err != nil {
		return err
	}
	delete(f.byID, id)
	f.audits = append(f.audits, audit)
	return nil
}

func (f *fakeStore) Visible(_ context.Context, scope tenant.Scope, nodes []identity.NodeID, groups []tenant.GroupID) (dashboards.Found, error) {
	found := dashboards.Found{Nodes: map[identity.NodeID]bool{}, Groups: map[tenant.GroupID]bool{}}
	for _, n := range nodes {
		if g, ok := f.nodes[n]; ok && scope.AllowsGroup(g) {
			found.Nodes[n] = true
		}
	}
	for _, g := range groups {
		if (g == groupX || g == groupY) && scope.AllowsGroup(g) {
			found.Groups[g] = true
		}
	}
	return found, nil
}

func (f *fakeStore) lastAudit() access.AuditEntry { return f.audits[len(f.audits)-1] }

var now = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

func newService(store *fakeStore) *dashboards.Service {
	return dashboards.NewService(store, authz.NewPolicy(), func() time.Time { return now }, uuid.New)
}

func caller(user identity.UserID, bindings ...authz.Binding) access.Caller {
	return access.Caller{Principal: authz.Principal{User: user, Org: orgA, Bindings: bindings}}
}

var (
	adminAll  = authz.Binding{AllGroups: true, Role: role.Admin}
	viewerX   = authz.Binding{Group: groupX, Role: role.Viewer}
	viewerAll = authz.Binding{AllGroups: true, Role: role.Viewer}
)

func spec(nodes ...identity.NodeID) dashboard.Spec {
	return dashboard.Spec{
		Version: 1, Title: "Web",
		Widgets: []dashboard.Widget{{
			ID: "cpu", Kind: dashboard.KindTimeseries, Title: "CPU", Position: dashboard.Position{W: 6, H: 4},
			Query: dashboard.Query{Metric: "sermon_cpu_utilization_ratio", Target: dashboard.Target{Nodes: nodes}},
		}},
	}
}

func TestCreate_StoresAPrivateDashboardOfTheCaller(t *testing.T) {
	store := newStore()
	svc := newService(store)
	d, err := svc.Create(context.Background(), caller(alice, viewerX), spec(nodeX))
	if err != nil {
		t.Fatal(err)
	}
	if d.Owner != alice || d.Shared || d.Revision != 1 || !d.CanEdit || !d.CreatedAt.Equal(now) {
		t.Errorf("created = %+v", d)
	}
	if a := store.lastAudit(); a.Action != "dashboard.create" || a.Actor != alice || a.Target != "dashboard:"+d.ID.String() {
		t.Errorf("audit = %+v", a)
	}
}

func TestCreate_Rejects(t *testing.T) {
	tests := []struct {
		name string
		c    access.Caller
		spec dashboard.Spec
		want error
		msg  string
	}{
		{"member without a role", caller(alice), spec(nodeX), access.ErrForbidden, ""},
		{"invalid document", caller(alice, viewerX), dashboard.Spec{Version: 2, Title: "x"}, dashboard.ErrInvalid, "version"},
		{"node of a group the caller can't view", caller(alice, viewerX), spec(nodeY), dashboards.ErrUnknownReference, `widget "cpu": unknown node`},
		{"node of no one", caller(alice, viewerAll), spec(identity.NodeID(uuid.New())), dashboards.ErrUnknownReference, `widget "cpu": unknown node`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newStore()
			_, err := newService(store).Create(context.Background(), tt.c, tt.spec)
			if !errors.Is(err, tt.want) || !strings.Contains(errText(err), tt.msg) {
				t.Fatalf("Create() = %v, want %v naming %q", err, tt.want, tt.msg)
			}
			if len(store.byID) != 0 {
				t.Error("nothing must be stored")
			}
		})
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestCreate_ChecksGroupsAndDefaultsOfVariables(t *testing.T) {
	svc := newService(newStore())
	s := spec(nodeX)
	s.Widgets[0].Query.Target = dashboard.Target{Group: &groupY}
	if _, err := svc.Create(context.Background(), caller(alice, viewerX), s); !errors.Is(err, dashboards.ErrUnknownReference) ||
		!strings.Contains(err.Error(), `widget "cpu": unknown group`) {
		t.Errorf("group of another: %v", err)
	}
	s = spec(nodeX)
	s.Variables = []dashboard.Variable{{Name: "node", Kind: dashboard.VariableNode, Default: uuid.UUID(nodeY)}}
	if _, err := svc.Create(context.Background(), caller(alice, viewerX), s); !errors.Is(err, dashboards.ErrUnknownReference) ||
		!strings.Contains(err.Error(), `variable "node": unknown default`) {
		t.Errorf("default of another: %v", err)
	}
	s.Variables[0].Default = uuid.UUID(nodeX)
	if _, err := svc.Create(context.Background(), caller(alice, viewerX), s); err != nil {
		t.Errorf("visible default: %v", err)
	}
}

func TestImport_IsAuditedAsImport(t *testing.T) {
	store := newStore()
	if _, err := newService(store).Import(context.Background(), caller(alice, viewerX), spec(nodeX)); err != nil {
		t.Fatal(err)
	}
	if a := store.lastAudit(); a.Action != "dashboard.import" {
		t.Errorf("audit = %+v", a)
	}
}

func TestListAndGet_ShowOwnAndSharedOnly(t *testing.T) {
	store := newStore()
	svc := newService(store)
	ctx := context.Background()
	own, _ := svc.Create(ctx, caller(alice, viewerX), spec(nodeX))
	private, _ := svc.Create(ctx, caller(bob, viewerX), spec(nodeX))
	shared, _ := svc.Create(ctx, caller(bob, viewerX), spec(nodeX))
	if _, err := svc.SetShared(ctx, caller(bob, viewerX), shared.ID, true); err != nil {
		t.Fatal(err)
	}

	list, err := svc.List(ctx, caller(alice, viewerX))
	if err != nil {
		t.Fatal(err)
	}
	var ids []dashboards.ID
	for _, d := range list {
		ids = append(ids, d.ID)
		if d.ID == shared.ID && d.CanEdit {
			t.Error("a viewer must not edit the shared dashboard of another")
		}
	}
	if !slices.Contains(ids, own.ID) || !slices.Contains(ids, shared.ID) || slices.Contains(ids, private.ID) {
		t.Errorf("list = %v, want own and shared, not the private one of bob", ids)
	}
	if _, err := svc.Get(ctx, caller(alice, viewerX), private.ID); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("private of another: %v, want NOT_FOUND", err)
	}
	if _, err := svc.Get(ctx, caller(alice, adminAll), private.ID); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("an admin must not see the private dashboard of another: %v", err)
	}
	if d, err := svc.Get(ctx, caller(alice, adminAll), shared.ID); err != nil || !d.CanEdit {
		t.Errorf("admin on a shared dashboard: %+v, %v; want can edit", d, err)
	}
	if _, err := svc.List(ctx, caller(carol)); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("member without a role: %v", err)
	}
}

func TestUpdate_RightsAndRevision(t *testing.T) {
	ctx := context.Background()
	setup := func() (*fakeStore, *dashboards.Service, dashboards.Dashboard) {
		store := newStore()
		svc := newService(store)
		d, err := svc.Create(ctx, caller(bob, viewerAll), spec(nodeX))
		if err != nil {
			t.Fatal(err)
		}
		return store, svc, d
	}
	changed := spec(nodeX, nodeY)
	changed.Title = "Changed"

	t.Run("owner", func(t *testing.T) {
		store, svc, d := setup()
		got, err := svc.Update(ctx, caller(bob, viewerAll), d.ID, d.Revision, changed)
		if err != nil || got.Revision != 2 || got.Spec.Title != "Changed" || !got.UpdatedAt.Equal(now) {
			t.Fatalf("Update() = %+v, %v", got, err)
		}
		if a := store.lastAudit(); a.Action != "dashboard.update" || a.Details["revision"] != "2" {
			t.Errorf("audit = %+v", a)
		}
	})
	t.Run("stale revision", func(t *testing.T) {
		_, svc, d := setup()
		if _, err := svc.Update(ctx, caller(bob, viewerAll), d.ID, d.Revision+1, changed); !errors.Is(err, dashboards.ErrConflict) {
			t.Errorf("Update() = %v, want ErrConflict", err)
		}
	})
	t.Run("private of another, even for an admin", func(t *testing.T) {
		_, svc, d := setup()
		if _, err := svc.Update(ctx, caller(alice, adminAll), d.ID, d.Revision, changed); !errors.Is(err, access.ErrNotFound) {
			t.Errorf("Update() = %v, want NOT_FOUND", err)
		}
	})
	t.Run("shared of another", func(t *testing.T) {
		_, svc, d := setup()
		if _, err := svc.SetShared(ctx, caller(bob, viewerAll), d.ID, true); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Update(ctx, caller(alice, viewerAll), d.ID, 2, changed); !errors.Is(err, access.ErrForbidden) {
			t.Errorf("viewer: %v, want PERMISSION_DENIED", err)
		}
		if _, err := svc.Update(ctx, caller(alice, authz.Binding{Group: groupX, Role: role.Admin}), d.ID, 2, spec(nodeX)); !errors.Is(err, access.ErrForbidden) {
			t.Errorf("admin of one group: %v, want PERMISSION_DENIED", err)
		}
		if _, err := svc.Update(ctx, caller(alice, adminAll), d.ID, 2, changed); err != nil {
			t.Errorf("admin of all groups: %v", err)
		}
	})
	t.Run("nodes the editor can't view", func(t *testing.T) {
		_, svc, d := setup()
		if _, err := svc.Update(ctx, caller(bob, viewerX), d.ID, d.Revision, changed); !errors.Is(err, dashboards.ErrUnknownReference) {
			t.Errorf("Update() = %v, want ErrUnknownReference", err)
		}
	})
	t.Run("invalid document", func(t *testing.T) {
		_, svc, d := setup()
		if _, err := svc.Update(ctx, caller(bob, viewerAll), d.ID, d.Revision, dashboard.Spec{Version: 1}); !errors.Is(err, dashboard.ErrInvalid) {
			t.Errorf("Update() = %v, want ErrInvalid", err)
		}
	})
}

func TestSetSharedAndDelete_NeedTheRightToEdit(t *testing.T) {
	ctx := context.Background()
	store := newStore()
	svc := newService(store)
	d, _ := svc.Create(ctx, caller(bob, viewerX), spec(nodeX))
	if _, err := svc.SetShared(ctx, caller(alice, adminAll), d.ID, true); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("share the private one of another: %v", err)
	}
	shared, err := svc.SetShared(ctx, caller(bob, viewerX), d.ID, true)
	if err != nil || !shared.Shared || shared.Revision != 2 {
		t.Fatalf("SetShared() = %+v, %v", shared, err)
	}
	if a := store.lastAudit(); a.Action != "dashboard.share" || a.Details["shared"] != "true" {
		t.Errorf("audit = %+v", a)
	}
	if _, err := svc.SetShared(ctx, caller(alice, viewerX), d.ID, false); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("viewer unshares: %v", err)
	}
	if err := svc.Delete(ctx, caller(alice, viewerX), d.ID); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("viewer deletes: %v", err)
	}
	if err := svc.Delete(ctx, caller(alice, adminAll), d.ID); err != nil {
		t.Errorf("admin deletes a shared one: %v", err)
	}
	if a := store.lastAudit(); a.Action != "dashboard.delete" || a.Actor != alice {
		t.Errorf("audit = %+v", a)
	}
	if len(store.byID) != 0 {
		t.Error("the dashboard must be gone")
	}
}

func TestCreate_AtTheLimitOfTheOrganization(t *testing.T) {
	store := newStore()
	svc := newService(store)
	for i := range dashboards.MaxPerOrganization {
		store.byID[dashboards.ID(uuid.New())] = dashboards.Dashboard{Owner: bob, Spec: dashboard.Spec{Title: string(rune('a' + i%26))}}
	}
	if _, err := svc.Create(context.Background(), caller(alice, viewerX), spec(nodeX)); !errors.Is(err, dashboards.ErrTooMany) {
		t.Errorf("Create() = %v, want ErrTooMany", err)
	}
}
