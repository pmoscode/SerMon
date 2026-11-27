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
	orgs     map[tenant.OrgID]*org
	audits   []access.AuditEntry
	expired  map[tenant.OrgID][]time.Time
	defaults []uint32
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

func (s *store) Organizations(context.Context) ([]orgretention.OrgSettings, error) {
	var out []orgretention.OrgSettings
	for id, o := range s.orgs {
		out = append(out, orgretention.OrgSettings{Org: id, Settings: o.settings, LogsErasedAt: o.erasedAt})
	}
	return out, nil
}

func (s *store) ExpireEvents(_ context.Context, id tenant.OrgID, before time.Time, defaultDays uint32) (int64, error) {
	s.expired[id] = append(s.expired[id], before)
	s.defaults = append(s.defaults, defaultDays)
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

// eventDefault is the instance's default of the tests (SERMON_EVENT_RETENTION).
const eventDefault = 60

func newService(s *store, e *eraser, c *clock) *orgretention.Service {
	return orgretention.NewService(s, authz.NewPolicy(), c.now, orgretention.WithLogs(e), orgretention.WithEventDefault(eventDefault))
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
	if err != nil || view.Settings != (retention.Settings{LogDays: 3, EventDays: 30}) || view.CapDays != 30 || view.EventDefaultDays != eventDefault {
		t.Errorf("Get = %+v, %v", view, err)
	}
	if s.orgs[orgB].settings != (retention.Settings{}) {
		t.Error("another organization changed")
	}
}

// The job deletes each organization's events before its own period – those of organizations without
// one before the instance's default (V1-22) – and asks Loki once a day per organization with a
// period of its own to delete its logs before it (Loki's retention is the default); a failing Loki
// is tried again in the next run.
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
	if got := s.expired[orgB]; len(got) != 2 || !got[1].Equal(now.Add(-eventDefault*24*time.Hour)) {
		t.Errorf("events of B with the defaults expired before %v, want %d days back", got, eventDefault)
	}
	for _, d := range s.defaults {
		if d != eventDefault {
			t.Fatalf("the database got the default %d, want %d", d, eventDefault)
		}
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
	if err := svc.Maintain(ctx); err != nil || len(e.calls) != 2 || len(s.expired[orgB]) != 5 ||
		!s.expired[orgB][4].Equal(c.t.Add(-7*24*time.Hour)) {
		t.Errorf("B with events only: %v, erasures %v, expired %v", err, e.calls, s.expired[orgB])
	}
}

// An organization that only shortens its logs keeps its events as long as the instance.
// Logs only set: the events follow the instance's default.
func TestService_MaintainKeepsTheDefaultForEventsOfLogsOnlySettings(t *testing.T) {
	s, e := newStore(), &eraser{}
	s.orgs[orgA].settings = retention.Settings{LogDays: 5}
	if err := newService(s, e, &clock{t: now}).Maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := s.expired[orgA]; len(got) != 1 || !got[0].Equal(now.Add(-eventDefault*24*time.Hour)) || len(e.calls) != 1 {
		t.Errorf("expired %v, erasures %v; want the default for events", got, e.calls)
	}
}

// Without WithEventDefault the default is the longest period (as before V1-22), never nothing.
func TestService_WithoutAnEventDefaultKeepsTheLongest(t *testing.T) {
	s := newStore()
	svc := orgretention.NewService(s, authz.NewPolicy(), func() time.Time { return now })
	if err := svc.Maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := s.expired[orgA]; len(got) != 1 || !got[0].Equal(now.Add(-retention.MaxEventDays*24*time.Hour)) {
		t.Errorf("expired %v, want %d days back", got, retention.MaxEventDays)
	}
	if view, err := svc.Get(context.Background(), admin); err != nil || view.EventDefaultDays != retention.MaxEventDays {
		t.Errorf("Get = %+v, %v", view, err)
	}
}
