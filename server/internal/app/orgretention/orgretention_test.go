package orgretention_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/orgretention"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/retention"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	orgA  = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
	orgB  = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000b"))
	group = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000aa"))
	now   = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
)

type org struct {
	settings retention.Settings
	capDays  uint32
	erasedAt *time.Time
}

// store behaves like the Postgres store: one organization per scope, the audit entry with the change.
type store struct {
	orgs    map[tenant.OrgID]*org
	audits  []access.AuditEntry
	expired map[tenant.OrgID][]time.Time
}

func newStore() *store {
	return &store{orgs: map[tenant.OrgID]*org{orgA: {}, orgB: {}}, expired: map[tenant.OrgID][]time.Time{}}
}

func (s *store) Get(_ context.Context, scope tenant.Scope) (retention.Settings, uint32, error) {
	o := s.orgs[scope.OrgID()]
	return o.settings, o.capDays, nil
}

func (s *store) Set(_ context.Context, scope tenant.Scope, set retention.Settings, audit access.AuditEntry) error {
	s.orgs[scope.OrgID()].settings = set
	s.audits = append(s.audits, audit)
	return nil
}

func (s *store) Configured(context.Context) ([]orgretention.OrgSettings, error) {
	var out []orgretention.OrgSettings
	for id, o := range s.orgs {
		if o.settings != (retention.Settings{}) {
			out = append(out, orgretention.OrgSettings{Org: id, Settings: o.settings, LogsErasedAt: o.erasedAt})
		}
	}
	return out, nil
}

func (s *store) ExpireEvents(_ context.Context, id tenant.OrgID, before time.Time) (int64, error) {
	s.expired[id] = append(s.expired[id], before)
	return 1, nil
}

func (s *store) MarkLogsErased(_ context.Context, id tenant.OrgID, at time.Time) error {
	s.orgs[id].erasedAt = &at
	return nil
}

type erasure struct {
	org    tenant.OrgID
	before time.Time
}

type eraser struct {
	calls []erasure
	fail  error
}

func (e *eraser) EraseBefore(_ context.Context, id tenant.OrgID, before time.Time) error {
	if e.fail != nil {
		return e.fail
	}
	e.calls = append(e.calls, erasure{id, before})
	return nil
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func caller(stepUp time.Duration, bindings ...authz.Binding) access.Caller {
	return access.Caller{
		Principal: authz.Principal{User: identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000f1")), Org: orgA, Bindings: bindings},
		StepUpAt:  now.Add(-stepUp),
	}
}

var admin = caller(time.Minute, authz.Binding{AllGroups: true, Role: role.Admin})

func newService(s *store, e *eraser, c *clock) *orgretention.Service {
	return orgretention.NewService(s, authz.NewPolicy(), c.now, orgretention.WithLogs(e))
}

// Only admins of the whole organization read and set it; setting needs a fresh step-up and stays
// within the instance and the cap; every change is audited.
func TestService_Rights(t *testing.T) {
	cases := map[string]struct {
		caller access.Caller
		want   error
	}{
		"org admin":            {admin, nil},
		"admin of one group":   {caller(time.Minute, authz.Binding{Group: group, Role: role.Admin}), access.ErrForbidden},
		"operator everywhere":  {caller(time.Minute, authz.Binding{AllGroups: true, Role: role.Operator}), access.ErrForbidden},
		"stale step-up to set": {caller(time.Hour, authz.Binding{AllGroups: true, Role: role.Admin}), access.ErrStepUpRequired},
	}
	for name, tc := range cases {
		s := newStore()
		svc := newService(s, &eraser{}, &clock{t: now})
		err := svc.Set(context.Background(), tc.caller, retention.Settings{LogDays: 3})
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: Set = %v, want %v", name, err, tc.want)
		}
		if tc.want != nil && (len(s.audits) != 0 || s.orgs[orgA].settings != (retention.Settings{})) {
			t.Errorf("%s: refused but changed", name)
		}
		if _, err := svc.Get(context.Background(), tc.caller); errors.Is(tc.want, access.ErrForbidden) && !errors.Is(err, access.ErrForbidden) {
			t.Errorf("%s: Get = %v, want forbidden", name, err)
		}
	}
}

func TestService_SetValidatesAndAudits(t *testing.T) {
	s := newStore()
	s.orgs[orgA].capDays = 30
	svc := newService(s, &eraser{}, &clock{t: now})
	ctx := context.Background()
	if err := svc.Set(ctx, admin, retention.Settings{EventDays: 90}); !errors.Is(err, retention.ErrInvalid) {
		t.Errorf("above the cap: %v", err)
	}
	if err := svc.Set(ctx, admin, retention.Settings{LogDays: 20}); !errors.Is(err, retention.ErrInvalid) {
		t.Errorf("beyond the instance: %v", err)
	}
	if err := svc.Set(ctx, admin, retention.Settings{LogDays: 3, EventDays: 30}); err != nil {
		t.Fatal(err)
	}
	if a := s.audits; len(a) != 1 || a[0].Action != "org.retention_set" || a[0].Target != "org:"+orgA.String() ||
		a[0].Details["retention"] != "logs=3d,events=30d" || a[0].Details["previous"] != "logs=default,events=default" {
		t.Errorf("audit = %+v", a)
	}
	view, err := svc.Get(ctx, admin)
	if err != nil || view.Settings != (retention.Settings{LogDays: 3, EventDays: 30}) || view.CapDays != 30 {
		t.Errorf("Get = %+v, %v", view, err)
	}
	if s.orgs[orgB].settings != (retention.Settings{}) {
		t.Error("another organization changed")
	}
}

// The job deletes each organization's events before its own period, and asks Loki once a day per
// organization to delete its logs before its period; organizations with the defaults are left to
// the instance's retention, a failing Loki is tried again in the next run.
func TestService_MaintainKeepsEachOrganizationsPeriod(t *testing.T) {
	s, e, c := newStore(), &eraser{}, &clock{t: now}
	s.orgs[orgA].settings = retention.Settings{LogDays: 3, EventDays: 30}
	svc := newService(s, e, c)
	ctx := context.Background()

	e.fail = errors.New("loki down")
	if err := svc.Maintain(ctx); err == nil {
		t.Error("a failing Loki went unreported")
	}
	if got := s.expired[orgA]; len(got) != 1 || !got[0].Equal(now.Add(-30*24*time.Hour)) {
		t.Errorf("events of A expired before %v, want 30 days back", got)
	}
	if s.orgs[orgA].erasedAt != nil {
		t.Fatal("marked erased although Loki failed")
	}
	e.fail = nil
	if err := svc.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if len(e.calls) != 1 || e.calls[0] != (erasure{orgA, now.Add(-3 * 24 * time.Hour)}) {
		t.Errorf("log erasures = %v, want A before 3 days", e.calls)
	}
	if len(s.expired[orgB]) != 0 {
		t.Errorf("B with the defaults was expired: %v", s.expired[orgB])
	}
	c.t = now.Add(23 * time.Hour)
	if err := svc.Maintain(ctx); err != nil || len(e.calls) != 1 {
		t.Errorf("within a day: %v, erasures %v", err, e.calls)
	}
	c.t = now.Add(24 * time.Hour)
	if err := svc.Maintain(ctx); err != nil || len(e.calls) != 2 || !e.calls[1].before.Equal(c.t.Add(-3*24*time.Hour)) {
		t.Errorf("a day later: %v, erasures %v", err, e.calls)
	}
	// Only events set: no logs asked for.
	s.orgs[orgB].settings = retention.Settings{EventDays: 7}
	if err := svc.Maintain(ctx); err != nil || len(e.calls) != 2 || len(s.expired[orgB]) != 1 {
		t.Errorf("B with events only: %v, erasures %v, expired %v", err, e.calls, s.expired[orgB])
	}
}

// An organization that only shortens its logs keeps its events as long as the instance.
func TestService_MaintainLeavesEventsOfLogsOnlySettings(t *testing.T) {
	s, e := newStore(), &eraser{}
	s.orgs[orgA].settings = retention.Settings{LogDays: 5}
	if err := newService(s, e, &clock{t: now}).Maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(s.expired[orgA]) != 0 || len(e.calls) != 1 {
		t.Errorf("expired %v, erasures %v; want logs only", s.expired[orgA], e.calls)
	}
}
