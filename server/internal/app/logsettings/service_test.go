package logsettings_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/logsettings"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/logconfig"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	org    = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
	other  = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000b"))
	web    = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000aa"))
	db     = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000ab"))
	admin  = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000101"))
	viewer = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000102"))
	now    = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
)

type memStore struct {
	groups  map[tenant.GroupID]string
	custom  map[tenant.GroupID]logconfig.Settings
	rules   []logconfig.Rule
	version uint64
	audits  []access.AuditEntry
	scopes  []tenant.Scope
}

func newStore() *memStore {
	return &memStore{groups: map[tenant.GroupID]string{web: "web", db: "db"}, custom: map[tenant.GroupID]logconfig.Settings{}, version: 1}
}

func (m *memStore) View(_ context.Context, scope tenant.Scope) (logsettings.View, error) {
	m.scopes = append(m.scopes, scope)
	v := logsettings.View{Rules: m.rules, Version: m.version}
	for id, name := range m.groups {
		s, ok := m.custom[id]
		if !ok {
			s = logconfig.Default()
		}
		v.Groups = append(v.Groups, logsettings.Group{ID: id, Name: name, Settings: s, Custom: ok})
	}
	return v, nil
}

func (m *memStore) PutGroup(_ context.Context, scope tenant.Scope, group tenant.GroupID, s *logconfig.Settings, _ identity.UserID, audit access.AuditEntry) error {
	m.scopes = append(m.scopes, scope)
	if _, ok := m.groups[group]; !ok {
		return access.ErrNotFound
	}
	if s == nil {
		delete(m.custom, group)
	} else {
		m.custom[group] = *s
	}
	m.version++
	m.audits = append(m.audits, audit)
	return nil
}

func (m *memStore) PutRules(_ context.Context, scope tenant.Scope, rules []logconfig.Rule, _ identity.UserID, audit access.AuditEntry) error {
	m.scopes = append(m.scopes, scope)
	m.rules = rules
	m.version++
	m.audits = append(m.audits, audit)
	return nil
}

type recorder struct{ changed []tenant.OrgID }

func (r *recorder) ConfigChanged(o tenant.OrgID) { r.changed = append(r.changed, o) }

type world struct {
	store  *memStore
	agents *recorder
	svc    *logsettings.Service
}

func newWorld() *world {
	w := &world{store: newStore(), agents: &recorder{}}
	w.svc = logsettings.NewService(w.store, authz.NewPolicy(), w.agents, func() time.Time { return now })
	return w
}

func caller(user identity.UserID, r role.Role, stepUp time.Time) access.Caller {
	return access.Caller{
		Principal: authz.Principal{User: user, Org: org, Bindings: []authz.Binding{{AllGroups: true, Role: r}}},
		StepUpAt:  stepUp,
	}
}

var (
	fresh = now.Add(-time.Minute)
	stale = now.Add(-time.Hour)
)

func TestView_OnlyAdminsSeeTheSettings(t *testing.T) {
	w := newWorld()
	view, err := w.svc.View(context.Background(), caller(admin, role.Admin, stale))
	if err != nil || len(view.Groups) != 2 || view.Version != 1 {
		t.Fatalf("View(admin) = %+v, %v", view, err)
	}
	for _, r := range []role.Role{role.Viewer, role.Operator} {
		if _, err := w.svc.View(context.Background(), caller(viewer, r, fresh)); !errors.Is(err, access.ErrForbidden) {
			t.Errorf("View(%v) = %v, want ErrForbidden", r, err)
		}
	}
	if w.store.scopes[0].OrgID() != org {
		t.Errorf("scope = %v", w.store.scopes[0])
	}
}

func TestSetGroup_NeedsAStepUpValidatesAuditsAndReachesTheAgents(t *testing.T) {
	w := newWorld()
	ctx := context.Background()
	settings := logconfig.Settings{Enabled: true, MinSeverity: logconfig.SeverityWarning, ExcludeUnits: []string{"cron.service"}}

	if err := w.svc.SetGroup(ctx, caller(admin, role.Admin, stale), web, settings); !errors.Is(err, access.ErrStepUpRequired) {
		t.Fatalf("stale step-up: %v", err)
	}
	if err := w.svc.SetGroup(ctx, caller(viewer, role.Operator, fresh), web, settings); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("operator: %v", err)
	}
	bad := logconfig.Settings{Files: []logconfig.FileGlob{{Pattern: "/var/log/../etc/shadow"}}}
	if err := w.svc.SetGroup(ctx, caller(admin, role.Admin, fresh), web, bad); !errors.Is(err, logconfig.ErrInvalid) {
		t.Fatalf("invalid: %v", err)
	}
	if len(w.store.audits) != 0 || len(w.agents.changed) != 0 {
		t.Fatalf("nothing may be stored or sent before: %+v %+v", w.store.audits, w.agents.changed)
	}

	if err := w.svc.SetGroup(ctx, caller(admin, role.Admin, fresh), web, settings); err != nil {
		t.Fatal(err)
	}
	if got := w.store.custom[web]; got.MinSeverity != logconfig.SeverityWarning || len(got.ExcludeUnits) != 1 {
		t.Errorf("stored %+v", got)
	}
	audit := w.store.audits[0]
	if audit.Actor != admin || audit.Action != "log_settings.update" || audit.Target != "group:"+web.String() || audit.Details["enabled"] != "true" {
		t.Errorf("audit = %+v", audit)
	}
	if len(w.agents.changed) != 1 || w.agents.changed[0] != org {
		t.Errorf("agents told = %v, want once for the organization", w.agents.changed)
	}

	unknown := tenant.GroupID(uuid.New())
	if err := w.svc.SetGroup(ctx, caller(admin, role.Admin, fresh), unknown, settings); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("unknown group: %v", err)
	}
	if len(w.agents.changed) != 1 {
		t.Errorf("a failed change reached the agents: %v", w.agents.changed)
	}
}

func TestResetGroup_ReturnsToTheDefaults(t *testing.T) {
	w := newWorld()
	ctx := context.Background()
	w.store.custom[db] = logconfig.Settings{}
	if err := w.svc.ResetGroup(ctx, caller(admin, role.Admin, stale), db); !errors.Is(err, access.ErrStepUpRequired) {
		t.Fatalf("stale step-up: %v", err)
	}
	if err := w.svc.ResetGroup(ctx, caller(admin, role.Admin, fresh), db); err != nil {
		t.Fatal(err)
	}
	if _, custom := w.store.custom[db]; custom || w.store.audits[0].Action != "log_settings.reset" || len(w.agents.changed) != 1 {
		t.Errorf("custom %v, audits %+v, agents %v", custom, w.store.audits, w.agents.changed)
	}
}

func TestSetRules_ValidatesAuditsWithoutPatternsAndReachesTheAgents(t *testing.T) {
	w := newWorld()
	ctx := context.Background()
	rules := []logconfig.Rule{{Pattern: `customer-\d{6}`}, {Pattern: `(?i)pin (?P<secret>\d{4})`}}

	if err := w.svc.SetRules(ctx, caller(admin, role.Admin, stale), rules); !errors.Is(err, access.ErrStepUpRequired) {
		t.Fatalf("stale step-up: %v", err)
	}
	if err := w.svc.SetRules(ctx, caller(admin, role.Admin, fresh), []logconfig.Rule{{Pattern: "(?=x)"}}); !errors.Is(err, logconfig.ErrInvalid) {
		t.Fatalf("invalid: %v", err)
	}
	if err := w.svc.SetRules(ctx, caller(admin, role.Admin, fresh), rules); err != nil {
		t.Fatal(err)
	}
	if len(w.store.rules) != 2 || w.store.version != 2 {
		t.Errorf("rules %+v, version %d", w.store.rules, w.store.version)
	}
	audit := w.store.audits[0]
	if audit.Action != "redaction_rules.update" || audit.Target != "org:"+org.String() || audit.Details["rules"] != "2" {
		t.Errorf("audit = %+v", audit)
	}
	for _, v := range audit.Details {
		if v == rules[0].Pattern {
			t.Errorf("audit names a pattern: %+v", audit)
		}
	}
	if len(w.agents.changed) != 1 {
		t.Errorf("agents told %v", w.agents.changed)
	}
}

func TestEveryChange_StaysInTheCallersOrganization(t *testing.T) {
	w := newWorld()
	foreign := access.Caller{
		Principal: authz.Principal{User: admin, Org: other, Bindings: []authz.Binding{{AllGroups: true, Role: role.Admin}}},
		StepUpAt:  fresh,
	}
	if err := w.svc.SetRules(context.Background(), foreign, nil); err != nil {
		t.Fatal(err)
	}
	if got := w.store.scopes[0].OrgID(); got != other {
		t.Errorf("scope = %v, want the caller's organization", got)
	}
	if w.agents.changed[0] != other {
		t.Errorf("agents told = %v", w.agents.changed)
	}
}
