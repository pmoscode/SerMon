package routing_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/app/routing"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	orgB = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000b"))
	ann  = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000e2"))
)

// policyStore keeps policies per organization, like RLS.
type policyStore struct {
	byOrg map[tenant.OrgID][]routing.Policy
	audit []access.AuditEntry
}

func (s *policyStore) Policies(_ context.Context, scope tenant.Scope) ([]routing.Policy, error) {
	return slices.Clone(s.byOrg[scope.OrgID()]), nil
}

func (s *policyStore) CreatePolicy(_ context.Context, scope tenant.Scope, p routing.Policy, a access.AuditEntry) error {
	if s.byOrg == nil {
		s.byOrg = map[tenant.OrgID][]routing.Policy{}
	}
	s.byOrg[scope.OrgID()] = append(s.byOrg[scope.OrgID()], p)
	s.audit = append(s.audit, a)
	return nil
}

func (s *policyStore) UpdatePolicy(_ context.Context, scope tenant.Scope, p routing.Policy, a access.AuditEntry) error {
	list := s.byOrg[scope.OrgID()]
	i := slices.IndexFunc(list, func(x routing.Policy) bool { return x.ID == p.ID })
	if i < 0 {
		return access.ErrNotFound
	}
	list[i] = p
	s.audit = append(s.audit, a)
	return nil
}

func (s *policyStore) DeletePolicy(_ context.Context, scope tenant.Scope, id routing.PolicyID, a access.AuditEntry) error {
	list := s.byOrg[scope.OrgID()]
	i := slices.IndexFunc(list, func(x routing.Policy) bool { return x.ID == id })
	if i < 0 {
		return access.ErrNotFound
	}
	s.byOrg[scope.OrgID()] = slices.Delete(list, i, i+1)
	s.audit = append(s.audit, a)
	return nil
}

// channels: chanA and chanB belong to orgA, chanC to orgB.
type channels struct{}

func (channels) Channels(_ context.Context, scope tenant.Scope) ([]notify.StoredChannel, error) {
	if scope.OrgID() == orgA {
		return []notify.StoredChannel{{Channel: notify.Channel{ID: chanA}}, {Channel: notify.Channel{ID: chanB}}}, nil
	}
	return []notify.StoredChannel{{Channel: notify.Channel{ID: chanC}}}, nil
}

type groups struct{}

func (groups) HasGroup(_ context.Context, scope tenant.Scope, g tenant.GroupID) (bool, error) {
	return scope.OrgID() == orgA && (g == groupA || g == groupB), nil
}

func service(s *policyStore, now time.Time) *routing.Service {
	return routing.NewService(routing.ServiceDeps{
		Store: s, Channels: channels{}, Groups: groups{}, Policy: authz.NewPolicy(),
		Now: func() time.Time { return now }, NewID: uuid.New,
	})
}

func caller(org tenant.OrgID, r role.Role, stepUp time.Time) access.Caller {
	return access.Caller{Principal: authz.Principal{User: ann, Org: org, Bindings: []authz.Binding{{AllGroups: true, Role: r}}}, StepUpAt: stepUp}
}

func newPolicy() routing.Policy {
	p := valid()
	p.Match.Groups = []tenant.GroupID{groupA}
	p.Escalation = &routing.Escalation{After: 15 * time.Minute, Channels: []notify.ChannelID{chanB}}
	return p
}

func TestService_CreateUpdateDelete(t *testing.T) {
	s := &policyStore{}
	svc := service(s, t0)
	admin := caller(orgA, role.Admin, t0)
	p, err := svc.Create(context.Background(), admin, newPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if p.ID == (routing.PolicyID{}) || p.Name != "Ops" || p.CreatedBy != ann || !p.CreatedAt.Equal(t0) || !p.UpdatedAt.Equal(t0) {
		t.Errorf("policy %+v", p)
	}
	change := newPolicy()
	change.Name, change.Enabled, change.ID, change.CreatedBy = "Security", false, routing.PolicyID(uuid.New()), alice
	later := service(s, t0.Add(time.Minute))
	got, err := later.Update(context.Background(), caller(orgA, role.Admin, t0.Add(time.Minute)), p.ID, change)
	if err != nil {
		t.Fatal(err)
	}
	// The ID and the creation stay; the rest is replaced.
	if got.ID != p.ID || got.Name != "Security" || got.Enabled || got.CreatedBy != ann || !got.CreatedAt.Equal(t0) || !got.UpdatedAt.Equal(t0.Add(time.Minute)) {
		t.Errorf("updated %+v", got)
	}
	list, err := svc.List(context.Background(), caller(orgA, role.Admin, time.Time{}))
	if err != nil || len(list) != 1 || list[0].Name != "Security" {
		t.Errorf("list %+v, %v", list, err)
	}
	if err := svc.Delete(context.Background(), admin, p.ID); err != nil {
		t.Fatal(err)
	}
	actions := []string{}
	for _, a := range s.audit {
		actions = append(actions, a.Action)
		if a.Actor != ann || a.Target != "notification_policy:"+p.ID.String() {
			t.Errorf("audit %+v", a)
		}
	}
	if !slices.Equal(actions, []string{"notification_policy.create", "notification_policy.update", "notification_policy.delete"}) {
		t.Errorf("audit %v", actions)
	}
}

// Policies decide where security alerts go: only admins, writing only with a fresh step-up.
func TestService_RightsAndStepUp(t *testing.T) {
	svc := service(&policyStore{}, t0)
	for _, r := range []role.Role{role.Operator, role.Viewer} {
		if _, err := svc.List(context.Background(), caller(orgA, r, t0)); !errors.Is(err, access.ErrForbidden) {
			t.Errorf("%s list: %v", r, err)
		}
		if _, err := svc.Create(context.Background(), caller(orgA, r, t0), newPolicy()); !errors.Is(err, access.ErrForbidden) {
			t.Errorf("%s create: %v", r, err)
		}
	}
	stale := caller(orgA, role.Admin, t0.Add(-access.StepUpMaxAge-time.Second))
	if _, err := svc.Create(context.Background(), stale, newPolicy()); !errors.Is(err, access.ErrStepUpRequired) {
		t.Errorf("create without step-up: %v", err)
	}
	if _, err := svc.Update(context.Background(), stale, routing.PolicyID(uuid.New()), newPolicy()); !errors.Is(err, access.ErrStepUpRequired) {
		t.Errorf("update without step-up: %v", err)
	}
	if err := svc.Delete(context.Background(), stale, routing.PolicyID(uuid.New())); !errors.Is(err, access.ErrStepUpRequired) {
		t.Errorf("delete without step-up: %v", err)
	}
}

func TestService_ChecksChannelsAndGroupsOfTheOrganization(t *testing.T) {
	svc := service(&policyStore{}, t0)
	admin := caller(orgA, role.Admin, t0)
	for name, change := range map[string]func(*routing.Policy){
		"invalid":                   func(p *routing.Policy) { p.Name = "" },
		"channel of another org":    func(p *routing.Policy) { p.Channels = []notify.ChannelID{chanC} },
		"escalation to another org": func(p *routing.Policy) { p.Escalation.Channels = []notify.ChannelID{chanC} },
		"unknown group":             func(p *routing.Policy) { p.Match.Groups = []tenant.GroupID{tenant.GroupID(uuid.New())} },
		"unknown channel":           func(p *routing.Policy) { p.Channels = []notify.ChannelID{notify.ChannelID(uuid.New())} },
	} {
		p := newPolicy()
		change(&p)
		if _, err := svc.Create(context.Background(), admin, p); !errors.Is(err, routing.ErrInvalidPolicy) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestService_IsolatesOrganizationsAndLimits(t *testing.T) {
	s := &policyStore{}
	svc := service(s, t0)
	p, err := svc.Create(context.Background(), caller(orgA, role.Admin, t0), newPolicy())
	if err != nil {
		t.Fatal(err)
	}
	other := caller(orgB, role.Admin, t0)
	moved := newPolicy()
	moved.Match.Groups, moved.Channels, moved.Escalation = nil, []notify.ChannelID{chanC}, nil
	if _, err := svc.Update(context.Background(), other, p.ID, moved); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("update in another org: %v", err)
	}
	if err := svc.Delete(context.Background(), other, p.ID); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("delete in another org: %v", err)
	}
	if list, _ := svc.List(context.Background(), other); len(list) != 0 {
		t.Errorf("other org sees %+v", list)
	}
	for len(s.byOrg[orgA]) < routing.MaxPolicies {
		s.byOrg[orgA] = append(s.byOrg[orgA], routing.Policy{ID: routing.PolicyID(uuid.New())})
	}
	if _, err := svc.Create(context.Background(), caller(orgA, role.Admin, t0), newPolicy()); !errors.Is(err, routing.ErrInvalidPolicy) {
		t.Errorf("policy %d: %v", routing.MaxPolicies+1, err)
	}
}

type windowStore struct {
	byOrg map[tenant.OrgID][]routing.MaintenanceWindow
	audit []string
}

func (s *windowStore) Windows(_ context.Context, scope tenant.Scope) ([]routing.MaintenanceWindow, error) {
	return slices.Clone(s.byOrg[scope.OrgID()]), nil
}

func (s *windowStore) CreateWindow(_ context.Context, scope tenant.Scope, w routing.MaintenanceWindow, a access.AuditEntry) error {
	if s.byOrg == nil {
		s.byOrg = map[tenant.OrgID][]routing.MaintenanceWindow{}
	}
	s.byOrg[scope.OrgID()] = append(s.byOrg[scope.OrgID()], w)
	s.audit = append(s.audit, a.Action)
	return nil
}

func (s *windowStore) UpdateWindow(_ context.Context, scope tenant.Scope, w routing.MaintenanceWindow, a access.AuditEntry) error {
	list := s.byOrg[scope.OrgID()]
	i := slices.IndexFunc(list, func(x routing.MaintenanceWindow) bool { return x.ID == w.ID })
	if i < 0 {
		return access.ErrNotFound
	}
	list[i] = w
	s.audit = append(s.audit, a.Action)
	return nil
}

func (s *windowStore) DeleteWindow(_ context.Context, scope tenant.Scope, id routing.WindowID, a access.AuditEntry) error {
	list := s.byOrg[scope.OrgID()]
	i := slices.IndexFunc(list, func(x routing.MaintenanceWindow) bool { return x.ID == id })
	if i < 0 {
		return access.ErrNotFound
	}
	s.byOrg[scope.OrgID()] = slices.Delete(list, i, i+1)
	s.audit = append(s.audit, a.Action)
	return nil
}

func windowService(w *windowStore, now time.Time) *routing.Service {
	return routing.NewService(routing.ServiceDeps{
		Store: &policyStore{}, Windows: w, Channels: channels{}, Groups: groups{}, Policy: authz.NewPolicy(),
		Now: func() time.Time { return now }, NewID: uuid.New,
	})
}

func TestService_MaintenanceWindows(t *testing.T) {
	ws := &windowStore{}
	svc := windowService(ws, t0)
	admin := caller(orgA, role.Admin, t0)
	w := routing.MaintenanceWindow{Name: " Patches ", Enabled: true, Groups: []tenant.GroupID{groupA}, Schedule: sundayNight()}
	created, err := svc.CreateWindow(context.Background(), admin, w)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == (routing.WindowID{}) || created.Name != "Patches" || created.CreatedBy != ann || !created.CreatedAt.Equal(t0) {
		t.Errorf("created %+v", created)
	}
	w.Name, w.Enabled = "Patches 2", false
	updated, err := svc.UpdateWindow(context.Background(), admin, created.ID, w)
	if err != nil || updated.ID != created.ID || updated.Name != "Patches 2" || !updated.CreatedAt.Equal(t0) || updated.CreatedBy != ann {
		t.Errorf("updated %+v, %v", updated, err)
	}
	if list, err := svc.ListWindows(context.Background(), caller(orgA, role.Admin, time.Time{})); err != nil || len(list) != 1 {
		t.Errorf("list %+v, %v", list, err)
	}
	for name, change := range map[string]func(*routing.MaintenanceWindow){
		"unknown group": func(w *routing.MaintenanceWindow) { w.Groups = []tenant.GroupID{tenant.GroupID(uuid.New())} },
		"invalid zone":  func(w *routing.MaintenanceWindow) { w.Schedule.TimeZone = "Nowhere/Land" },
	} {
		bad := w
		change(&bad)
		if _, err := svc.CreateWindow(context.Background(), admin, bad); !errors.Is(err, routing.ErrInvalidWindow) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Admins only, writing with a fresh step-up; other organizations find nothing.
	if _, err := svc.ListWindows(context.Background(), caller(orgA, role.Operator, t0)); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("operator: %v", err)
	}
	if _, err := svc.CreateWindow(context.Background(), caller(orgA, role.Admin, t0.Add(-time.Hour)), w); !errors.Is(err, access.ErrStepUpRequired) {
		t.Errorf("stale step-up: %v", err)
	}
	if err := svc.DeleteWindow(context.Background(), caller(orgB, role.Admin, t0), created.ID); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("foreign delete: %v", err)
	}
	if err := svc.DeleteWindow(context.Background(), admin, created.ID); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ws.audit, []string{"maintenance_window.create", "maintenance_window.update", "maintenance_window.delete"}) {
		t.Errorf("audit %v", ws.audit)
	}
}
