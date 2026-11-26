package modules_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/modules"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/module"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	org    = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
	web    = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000aa"))
	db     = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000ab"))
	admin  = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000101"))
	viewer = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000102"))
	node1  = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000201"))
	now    = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
)

const nginx = `{"type":"object","additionalProperties":false,"required":["url"],"properties":{
"url":{"type":"string","format":"uri","maxLength":512},
"password":{"type":"string","writeOnly":true,"maxLength":64}}}`

type memStore struct {
	groups  map[tenant.GroupID][]module.Setting
	nodes   map[identity.NodeID]modules.Node
	hints   []modules.NodeHint
	version uint64
	audits  []access.AuditEntry
	scopes  []tenant.Scope
}

func newStore() *memStore {
	return &memStore{
		groups: map[tenant.GroupID][]module.Setting{web: nil, db: nil}, version: 1,
		nodes: map[identity.NodeID]modules.Node{node1: {
			ID: node1, Group: web, Builtin: []string{"nginx", "simulate"},
			Settings: []module.Setting{{Module: "nginx", Enabled: true}, {Module: "redis", Enabled: true}},
		}},
		hints: []modules.NodeHint{{Node: node1, Group: web, Hint: module.Hint{Module: "nginx"}}},
	}
}

func (m *memStore) View(_ context.Context, scope tenant.Scope) (modules.View, error) {
	m.scopes = append(m.scopes, scope)
	v := modules.View{Version: m.version}
	for id, s := range m.groups {
		v.Groups = append(v.Groups, modules.Group{ID: id, Modules: s})
	}
	return v, nil
}

func (m *memStore) Put(_ context.Context, scope tenant.Scope, group tenant.GroupID, s module.Setting, _ identity.UserID, audit access.AuditEntry) error {
	m.scopes = append(m.scopes, scope)
	settings, ok := m.groups[group]
	if !ok {
		return access.ErrNotFound
	}
	settings = slices.DeleteFunc(settings, func(o module.Setting) bool { return o.Module == s.Module })
	m.groups[group] = append(settings, s)
	m.version++
	m.audits = append(m.audits, audit)
	return nil
}

func (m *memStore) Remove(_ context.Context, scope tenant.Scope, group tenant.GroupID, id string, _ identity.UserID, audit access.AuditEntry) error {
	m.scopes = append(m.scopes, scope)
	settings, ok := m.groups[group]
	if !ok || !slices.ContainsFunc(settings, func(o module.Setting) bool { return o.Module == id }) {
		return access.ErrNotFound
	}
	m.groups[group] = slices.DeleteFunc(settings, func(o module.Setting) bool { return o.Module == id })
	m.version++
	m.audits = append(m.audits, audit)
	return nil
}

func (m *memStore) Node(_ context.Context, scope tenant.Scope, id identity.NodeID) (modules.Node, error) {
	m.scopes = append(m.scopes, scope)
	n, ok := m.nodes[id]
	if !ok || !scope.AllowsGroup(n.Group) {
		return modules.Node{}, access.ErrNotFound
	}
	return n, nil
}

func (m *memStore) OpenHints(_ context.Context, scope tenant.Scope, limit int) ([]modules.NodeHint, error) {
	m.scopes = append(m.scopes, scope)
	var out []modules.NodeHint
	for _, h := range m.hints {
		if scope.AllowsGroup(h.Group) && len(out) < limit {
			out = append(out, h)
		}
	}
	return out, nil
}

type agents struct{ changed []tenant.OrgID }

func (a *agents) ConfigChanged(o tenant.OrgID) { a.changed = append(a.changed, o) }

func caller(user identity.UserID, stepUp time.Time, bindings ...authz.Binding) access.Caller {
	return access.Caller{Principal: authz.Principal{User: user, Org: org, Bindings: bindings}, StepUpAt: stepUp}
}

func adminCaller() access.Caller {
	return caller(admin, now.Add(-time.Minute), authz.Binding{AllGroups: true, Role: role.Admin})
}

func setup(t *testing.T) (*modules.Service, *memStore, *agents) {
	t.Helper()
	catalog, err := module.NewCatalog(map[string]string{"nginx": nginx})
	if err != nil {
		t.Fatal(err)
	}
	store, a := newStore(), &agents{}
	return modules.NewService(store, catalog, authz.NewPolicy(), a, func() time.Time { return now }), store, a
}

func TestSetGroupModule_StoresAuditsAndTellsTheAgents(t *testing.T) {
	svc, store, a := setup(t)
	err := svc.SetGroupModule(context.Background(), adminCaller(), web, module.Setting{
		Module: "nginx", Enabled: true, Interval: 20 * time.Second, Config: ` {"url": "http://127.0.0.1/status"} `,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := store.groups[web]
	if len(got) != 1 || got[0].Config != `{"url":"http://127.0.0.1/status"}` || !got[0].Enabled || got[0].Interval != 20*time.Second {
		t.Fatalf("stored %+v", got)
	}
	if len(store.audits) != 1 || store.audits[0].Action != "module.update" || store.audits[0].Target != "group:"+web.String() ||
		store.audits[0].Details["module"] != "nginx" || store.audits[0].Details["interval_seconds"] != "20" || store.audits[0].Details["enabled"] != "true" {
		t.Errorf("audit %+v", store.audits)
	}
	if !slices.Equal(a.changed, []tenant.OrgID{org}) || !store.scopes[0].AllGroups() || store.scopes[0].OrgID() != org {
		t.Errorf("agents %v, scope %+v", a.changed, store.scopes)
	}
}

func TestSetGroupModule_RefusesWhatTheAgentWouldRefuse(t *testing.T) {
	svc, store, a := setup(t)
	for _, tc := range []struct {
		setting module.Setting
		want    error
		why     string
	}{
		{module.Setting{Module: "nginx", Config: `{}`}, module.ErrInvalid, "url: required"},
		{module.Setting{Module: "nginx", Config: `{"url":"http://x/","password":"pw"}`}, module.ErrInvalid, "credential"},
		{module.Setting{Module: "nginx", Config: `{"url":"ftp://x/"}`}, module.ErrInvalid, "url"},
		{module.Setting{Module: "nginx", Config: `{"url":"http://x/"}`, Interval: 5 * time.Second}, module.ErrInvalid, "interval"},
		{module.Setting{Module: "redis", Config: `{}`}, access.ErrNotFound, ""},
	} {
		err := svc.SetGroupModule(context.Background(), adminCaller(), web, tc.setting)
		if !errors.Is(err, tc.want) || !strings.Contains(err.Error(), tc.why) {
			t.Errorf("%+v: %v, want %v %q", tc.setting, err, tc.want, tc.why)
		}
	}
	if len(store.audits) != 0 || len(a.changed) != 0 || len(store.scopes) != 0 {
		t.Errorf("stored %+v, told %v", store.audits, a.changed)
	}
}

func TestSettings_NeedAnAdminForAllGroupsAndAStepUp(t *testing.T) {
	svc, store, a := setup(t)
	ok := module.Setting{Module: "nginx", Config: `{"url":"http://x/"}`}
	ctx := context.Background()
	for name, c := range map[string]access.Caller{
		"viewer":         caller(viewer, now, authz.Binding{AllGroups: true, Role: role.Viewer}),
		"operator":       caller(viewer, now, authz.Binding{AllGroups: true, Role: role.Operator}),
		"group admin":    caller(viewer, now, authz.Binding{Group: web, Role: role.Admin}),
		"no principal":   {},
		"stale step-up?": caller(admin, now.Add(-access.StepUpMaxAge-time.Second), authz.Binding{AllGroups: true, Role: role.Admin}),
	} {
		wantSet := access.ErrForbidden
		if name == "stale step-up?" {
			wantSet = access.ErrStepUpRequired
			if _, err := svc.View(ctx, c); err != nil {
				t.Errorf("%s: reading needs no step-up: %v", name, err)
			}
		} else if _, err := svc.View(ctx, c); !errors.Is(err, access.ErrForbidden) {
			t.Errorf("%s: View = %v", name, err)
		}
		if err := svc.SetGroupModule(ctx, c, web, ok); !errors.Is(err, wantSet) {
			t.Errorf("%s: Set = %v", name, err)
		}
		if err := svc.RemoveGroupModule(ctx, c, web, "nginx"); !errors.Is(err, wantSet) {
			t.Errorf("%s: Remove = %v", name, err)
		}
	}
	if len(store.audits) != 0 || len(a.changed) != 0 {
		t.Errorf("changed: %+v %v", store.audits, a.changed)
	}
}

func TestRemoveGroupModule(t *testing.T) {
	svc, store, a := setup(t)
	ctx := context.Background()
	if err := svc.SetGroupModule(ctx, adminCaller(), db, module.Setting{Module: "nginx", Config: `{"url":"http://x/"}`}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RemoveGroupModule(ctx, adminCaller(), db, "nginx"); err != nil {
		t.Fatal(err)
	}
	if len(store.groups[db]) != 0 || len(a.changed) != 2 || store.audits[1].Action != "module.remove" || store.audits[1].Details["module"] != "nginx" {
		t.Errorf("%+v %v %+v", store.groups[db], a.changed, store.audits)
	}
	for _, id := range []string{"nginx", "Bad"} {
		if err := svc.RemoveGroupModule(ctx, adminCaller(), db, id); !errors.Is(err, access.ErrNotFound) {
			t.Errorf("%s: %v", id, err)
		}
	}
}

func TestNode_ShowsOnlyWhatTheAgentHasBuiltIn(t *testing.T) {
	svc, _, _ := setup(t)
	n, err := svc.Node(context.Background(), caller(viewer, time.Time{}, authz.Binding{Group: web, Role: role.Viewer}), node1)
	if err != nil {
		t.Fatal(err)
	}
	if len(n.Settings) != 1 || n.Settings[0].Module != "nginx" {
		t.Errorf("settings %+v: redis is not built in", n.Settings)
	}
	for name, c := range map[string]access.Caller{
		"other group": caller(viewer, now, authz.Binding{Group: db, Role: role.Admin}),
		"nobody":      caller(viewer, now),
	} {
		if _, err := svc.Node(context.Background(), c, node1); !errors.Is(err, access.ErrNotFound) && !errors.Is(err, access.ErrForbidden) {
			t.Errorf("%s: %v", name, err)
		}
		if name == "other group" {
			if _, err := svc.Node(context.Background(), c, node1); !errors.Is(err, access.ErrNotFound) {
				t.Errorf("%s: %v, want NotFound", name, err)
			}
		}
	}
}

func TestHintsAndModules_ForViewersOfTheirGroups(t *testing.T) {
	svc, store, _ := setup(t)
	ctx := context.Background()
	hints, err := svc.Hints(ctx, caller(viewer, time.Time{}, authz.Binding{Group: web, Role: role.Viewer}))
	if err != nil || len(hints) != 1 || hints[0].Hint.Module != "nginx" {
		t.Fatalf("%+v %v", hints, err)
	}
	if last := store.scopes[len(store.scopes)-1]; last.AllGroups() || !slices.Equal(last.Groups(), []tenant.GroupID{web}) {
		t.Errorf("scope %+v", last)
	}
	if hints, _ := svc.Hints(ctx, caller(viewer, time.Time{}, authz.Binding{Group: db, Role: role.Viewer})); len(hints) != 0 {
		t.Errorf("other group sees %+v", hints)
	}
	list, err := svc.Modules(ctx, caller(viewer, time.Time{}, authz.Binding{Group: db, Role: role.Viewer}))
	if err != nil || len(list) != 1 || list[0].ID != "nginx" {
		t.Errorf("%+v %v", list, err)
	}
	if _, err := svc.Modules(ctx, caller(viewer, time.Time{})); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("without a role: %v", err)
	}
	if _, err := svc.Hints(ctx, caller(viewer, time.Time{})); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("without a role: %v", err)
	}
}
