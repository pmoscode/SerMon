package secevents_test

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/secevents"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/secevent"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	org    = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
	groupX = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000aa"))
	user   = authz.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001"))
	node   = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000002"))
	t0     = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
)

// store returns n events, newest first, and records the call.
type store struct {
	n      int
	scope  tenant.Scope
	filter secevents.Filter
}

func (s *store) List(_ context.Context, scope tenant.Scope, f secevents.Filter) ([]secevent.Event, error) {
	s.scope, s.filter = scope, f
	out := make([]secevent.Event, 0, min(s.n, f.Limit))
	for i := range min(s.n, f.Limit) {
		out = append(out, secevent.Event{ID: int64(1000 - i), Node: node, OccurredAt: t0.Add(-time.Duration(i) * time.Second), Kind: secevent.SSHLoginFailed})
	}
	return out, nil
}

func caller(r role.Role, all bool) access.Caller {
	b := authz.Binding{Group: groupX, Role: r, AllGroups: all}
	return access.Caller{Principal: authz.Principal{User: user, Org: org, Bindings: []authz.Binding{b}}}
}

func service(s *store, opts ...authz.Option) *secevents.Service {
	return secevents.NewService(s, authz.NewPolicy(opts...))
}

func request() secevents.Request {
	return secevents.Request{Filter: secevents.Filter{From: t0.Add(-time.Hour), To: t0}}
}

func TestList_InTheScopeOfTheAction(t *testing.T) {
	s := &store{n: 3}
	page, err := service(s).List(context.Background(), caller(role.Viewer, false), request())
	if err != nil || len(page.Events) != 3 || page.Next != "" {
		t.Fatalf("List() = %+v, %v", page, err)
	}
	if !s.scope.AllowsGroup(groupX) || s.scope.AllGroups() || s.filter.Limit != secevents.DefaultPageSize+1 {
		t.Errorf("scope %+v, filter %+v", s.scope, s.filter)
	}
	// The organization keeps logs and security events from viewers.
	_, err = service(&store{}, authz.WithViewerLogsDisabled(org)).List(context.Background(), caller(role.Viewer, true), request())
	if !errors.Is(err, access.ErrForbidden) {
		t.Errorf("viewer without logs: %v, want forbidden", err)
	}
	if _, err := service(&store{}, authz.WithViewerLogsDisabled(org)).List(context.Background(), caller(role.Operator, false), request()); err != nil {
		t.Errorf("operator: %v", err)
	}
}

func TestList_Pages(t *testing.T) {
	s := &store{n: 10}
	svc := service(s)
	req := request()
	req.PageSize = 4
	page, err := svc.List(context.Background(), caller(role.Viewer, true), req)
	if err != nil || len(page.Events) != 4 || page.Next == "" {
		t.Fatalf("first page = %+v, %v", page, err)
	}
	req.PageToken = page.Next
	if _, err := svc.List(context.Background(), caller(role.Viewer, true), req); err != nil {
		t.Fatal(err)
	}
	last := page.Events[3]
	if b := s.filter.Before; b == nil || b.ID != last.ID || !b.At.Equal(last.OccurredAt) {
		t.Errorf("cursor = %+v, want after event %d", b, last.ID)
	}
}

func TestList_RejectsBadFilters(t *testing.T) {
	long := string(make([]byte, 129))
	for name, change := range map[string]func(*secevents.Request){
		"to before from":  func(r *secevents.Request) { r.To = r.From.Add(-time.Second) },
		"over a year":     func(r *secevents.Request) { r.From = r.To.Add(-367 * 24 * time.Hour) },
		"no range":        func(r *secevents.Request) { r.From, r.To = time.Time{}, time.Time{} },
		"page size":       func(r *secevents.Request) { r.PageSize = 501 },
		"broken token":    func(r *secevents.Request) { r.PageToken = "not-a-token" },
		"unknown kind":    func(r *secevents.Request) { r.Kinds = []secevent.Kind{"ssh.root"} },
		"long user":       func(r *secevents.Request) { r.User = &long },
		"too many nodes":  func(r *secevents.Request) { r.Nodes = make([]identity.NodeID, 101) },
		"network not set": func(r *secevents.Request) { p := netip.Prefix{}; r.Network = &p },
	} {
		req := request()
		change(&req)
		if _, err := service(&store{}).List(context.Background(), caller(role.Admin, true), req); !errors.Is(err, secevent.ErrInvalid) {
			t.Errorf("%s: List() = %v, want invalid", name, err)
		}
	}
}

func TestParseNetwork(t *testing.T) {
	for in, want := range map[string]string{
		"192.0.2.7":        "192.0.2.7/32",
		"192.0.2.77/24":    "192.0.2.0/24",
		"2001:db8::1":      "2001:db8::1/128",
		"2001:db8::/32":    "2001:db8::/32",
		"::ffff:192.0.2.7": "192.0.2.7/32",
	} {
		got, err := secevents.ParseNetwork(in)
		if err != nil || got.String() != want {
			t.Errorf("ParseNetwork(%q) = %v, %v; want %s", in, got, err, want)
		}
	}
	for _, in := range []string{"192.0.2.300", "example.org", "10.0.0.0/33", "fe80::1%eth0"} {
		if _, err := secevents.ParseNetwork(in); !errors.Is(err, secevent.ErrInvalid) {
			t.Errorf("ParseNetwork(%q) = %v", in, err)
		}
	}
}

type partitions struct {
	horizon time.Time
	expired []time.Time
	cancel  context.CancelFunc
}

func (p *partitions) Horizon(context.Context) (time.Time, error) { return p.horizon, nil }

func (p *partitions) Expire(_ context.Context, before time.Time) (int, error) {
	p.expired = append(p.expired, before)
	p.cancel()
	return 0, errors.New("database down") // logged, the loop goes on until ctx ends
}

func TestMaintenance_KeepsAYearAndWarnsBeforeTheHorizon(t *testing.T) {
	for horizon, warn := range map[time.Time]bool{t0.AddDate(10, 0, 0): false, t0.AddDate(0, 11, 0): true} {
		ctx, cancel := context.WithCancel(context.Background())
		p := &partitions{horizon: horizon, cancel: cancel}
		var log strings.Builder
		if err := secevents.Maintenance(ctx, p, func() time.Time { return t0 }, time.Hour, slog.New(slog.NewTextHandler(&log, nil))); err != nil {
			t.Fatal(err)
		}
		if len(p.expired) != 1 || !p.expired[0].Equal(t0.AddDate(-1, 0, 0)) {
			t.Errorf("expired %v", p.expired)
		}
		if got := strings.Contains(log.String(), "end soon"); got != warn {
			t.Errorf("horizon %v: warning %v, want %v\n%s", horizon, got, warn, log.String())
		}
	}
}
