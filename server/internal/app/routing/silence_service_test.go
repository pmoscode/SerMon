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
	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	"github.com/pmoscode/SerMon/server/internal/app/routing"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

type silenceStore struct {
	byOrg map[tenant.OrgID][]routing.Silence
	audit []access.AuditEntry
	purge time.Time
}

func (s *silenceStore) Silences(_ context.Context, scope tenant.Scope, since time.Time) ([]routing.Silence, error) {
	var out []routing.Silence
	for _, x := range s.byOrg[scope.OrgID()] {
		if x.EndsAt.After(since) {
			out = append(out, x)
		}
	}
	return out, nil
}

func (s *silenceStore) Silence(_ context.Context, scope tenant.Scope, id routing.SilenceID) (routing.Silence, error) {
	for _, x := range s.byOrg[scope.OrgID()] {
		if x.ID == id {
			return x, nil
		}
	}
	return routing.Silence{}, access.ErrNotFound
}

func (s *silenceStore) CreateSilence(_ context.Context, scope tenant.Scope, x routing.Silence, purge time.Time, a access.AuditEntry) error {
	if s.byOrg == nil {
		s.byOrg = map[tenant.OrgID][]routing.Silence{}
	}
	s.byOrg[scope.OrgID()] = append(s.byOrg[scope.OrgID()], x)
	s.audit, s.purge = append(s.audit, a), purge
	return nil
}

func (s *silenceStore) ExpireSilence(_ context.Context, scope tenant.Scope, id routing.SilenceID, at time.Time, by identity.UserID, a access.AuditEntry) error {
	list := s.byOrg[scope.OrgID()]
	for i := range list {
		if list[i].ID == id && list[i].EndsAt.After(at) {
			list[i].EndsAt, list[i].ExpiredBy = at, by
			if at.Before(list[i].StartsAt) {
				list[i].StartsAt = at
			}
			s.audit = append(s.audit, a)
			return nil
		}
	}
	return access.ErrNotFound
}

// directory: web in groupA, db in groupB (orgA).
type directory struct{}

func (directory) List(_ context.Context, scope tenant.Scope) ([]fleet.Node, error) {
	var out []fleet.Node
	for _, n := range nodes {
		if scope.OrgID() == orgA && scope.AllowsGroup(n.Group) {
			out = append(out, n)
		}
	}
	return out, nil
}

func (directory) HasGroup(_ context.Context, scope tenant.Scope, g tenant.GroupID) (bool, error) {
	return scope.OrgID() == orgA && (g == groupA || g == groupB), nil
}

func silences(s *silenceStore, now time.Time) *routing.SilenceService {
	return routing.NewSilenceService(routing.SilenceDeps{Store: s, Nodes: directory{}, Policy: authz.NewPolicy(), Now: func() time.Time { return now }, NewID: uuid.New})
}

// groupCaller has a role on one group only.
func groupCaller(r role.Role, g tenant.GroupID, stepUp time.Time) access.Caller {
	return access.Caller{Principal: authz.Principal{User: ann, Org: orgA, Bindings: []authz.Binding{{Group: g, Role: r}}}, StepUpAt: stepUp}
}

func TestSilences_CreateAndExpire(t *testing.T) {
	s := &silenceStore{}
	svc := silences(s, t0)
	op := groupCaller(role.Operator, groupA, t0)
	in := webSilence()
	in.StartsAt = time.Time{} // now
	created, err := svc.Create(context.Background(), op, in)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == (routing.SilenceID{}) || !created.StartsAt.Equal(t0) || created.CreatedBy != ann || !created.CreatedAt.Equal(t0) {
		t.Errorf("created %+v", created)
	}
	if !s.purge.Equal(t0.Add(-30*24*time.Hour)) || s.audit[0].Action != "alert_silence.create" || s.audit[0].Target != "alert_silence:"+created.ID.String() {
		t.Errorf("purge %v, audit %+v", s.purge, s.audit)
	}
	later := silences(s, t0.Add(10*time.Minute))
	if err := later.Expire(context.Background(), groupCaller(role.Operator, groupA, time.Time{}), created.ID); err != nil {
		t.Fatalf("expire without step-up: %v", err)
	}
	got, _ := s.Silence(context.Background(), mustScope(t, orgA), created.ID)
	if !got.EndsAt.Equal(t0.Add(10*time.Minute)) || got.ExpiredBy != ann {
		t.Errorf("expired %+v", got)
	}
	if err := later.Expire(context.Background(), op, created.ID); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("second expire: %v", err)
	}
}

// Operators mute only their groups; muting all groups needs the right on all of them, and a
// fresh step-up keeps a hijacked session from muting the security alerts.
func TestSilences_RightsPerGroup(t *testing.T) {
	svc := silences(&silenceStore{}, t0)
	ctx := context.Background()
	other := webSilence()
	other.Match.Groups, other.Nodes = []tenant.GroupID{groupB}, nil
	everywhere := routing.Silence{Match: routing.Matchers{Rules: []string{"security.*"}}, StartsAt: t0, EndsAt: t0.Add(time.Hour), Comment: "noisy"}
	for name, tc := range map[string]struct {
		c   access.Caller
		s   routing.Silence
		err error
	}{
		"viewer":                {groupCaller(role.Viewer, groupA, t0), webSilence(), access.ErrForbidden},
		"another group":         {groupCaller(role.Operator, groupA, t0), other, access.ErrForbidden},
		"all groups, one right": {groupCaller(role.Operator, groupA, t0), everywhere, access.ErrForbidden},
		"stale step-up":         {groupCaller(role.Operator, groupA, t0.Add(-time.Hour)), webSilence(), access.ErrStepUpRequired},
		"all groups":            {caller(orgA, role.Operator, t0), everywhere, nil},
		"another organization":  {caller(orgB, role.Operator, t0), webSilence(), routing.ErrInvalidSilence},
	} {
		if _, err := svc.Create(ctx, tc.c, tc.s); !errors.Is(err, tc.err) && (tc.err != nil || err != nil) {
			t.Errorf("%s: %v, want %v", name, err, tc.err)
		}
	}
}

func TestSilences_ChecksNodesGroupsAndLimits(t *testing.T) {
	s := &silenceStore{}
	svc := silences(s, t0)
	op := caller(orgA, role.Operator, t0)
	for name, change := range map[string]func(*routing.Silence){
		"node of another group": func(x *routing.Silence) { x.Nodes = []identity.NodeID{db} },
		"unknown node":          func(x *routing.Silence) { x.Nodes = []identity.NodeID{identity.NodeID(uuid.New())} },
		"unknown group":         func(x *routing.Silence) { x.Match.Groups, x.Nodes = []tenant.GroupID{tenant.GroupID(uuid.New())}, nil },
		"in the past":           func(x *routing.Silence) { x.StartsAt, x.EndsAt = t0.Add(-2*time.Hour), t0.Add(-time.Hour) },
	} {
		x := webSilence()
		change(&x)
		if _, err := svc.Create(context.Background(), op, x); !errors.Is(err, routing.ErrInvalidSilence) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for len(s.byOrg[orgA]) < routing.MaxOpenSilences {
		x := webSilence()
		x.ID = routing.SilenceID(uuid.New())
		s.byOrg = map[tenant.OrgID][]routing.Silence{orgA: append(s.byOrg[orgA], x)}
	}
	if _, err := svc.Create(context.Background(), op, webSilence()); !errors.Is(err, routing.ErrInvalidSilence) {
		t.Errorf("silence %d: %v", routing.MaxOpenSilences+1, err)
	}
}

// Members see the silences of their groups and those for all groups (which name no nodes);
// ended ones for a day.
func TestSilences_ListShowsWhatTheCallerMaySee(t *testing.T) {
	s := &silenceStore{}
	svc := silences(s, t0)
	admin := caller(orgA, role.Admin, t0)
	mk := func(groups []tenant.GroupID, ends time.Time) routing.SilenceID {
		x := routing.Silence{Match: routing.Matchers{Groups: groups, Rules: []string{"system.*"}}, StartsAt: t0, EndsAt: ends, Comment: "c"}
		created, err := svc.Create(context.Background(), admin, x)
		if err != nil {
			t.Fatal(err)
		}
		return created.ID
	}
	a := mk([]tenant.GroupID{groupA}, t0.Add(time.Hour))
	b := mk([]tenant.GroupID{groupB}, t0.Add(time.Hour))
	ab := mk([]tenant.GroupID{groupA, groupB}, t0.Add(time.Hour))
	all := mk(nil, t0.Add(time.Hour))
	if err := svc.Expire(context.Background(), admin, all); err != nil {
		t.Fatal(err)
	}
	ids := func(c access.Caller, at time.Time) []routing.SilenceID {
		list, err := silences(s, at).List(context.Background(), c)
		if err != nil {
			t.Fatal(err)
		}
		var out []routing.SilenceID
		for _, x := range list {
			out = append(out, x.ID)
		}
		return out
	}
	if got := ids(groupCaller(role.Viewer, groupA, time.Time{}), t0); !slices.Equal(got, []routing.SilenceID{a, all}) {
		t.Errorf("viewer of A sees %v", got)
	}
	if got := ids(admin, t0); len(got) != 4 || !slices.Contains(got, ab) || !slices.Contains(got, b) {
		t.Errorf("admin sees %v", got)
	}
	if got := ids(admin, t0.Add(26*time.Hour)); len(got) != 0 {
		t.Errorf("after a day: %v", got)
	}
}
