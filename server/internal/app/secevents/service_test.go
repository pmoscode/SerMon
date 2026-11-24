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

// store returns n events, newest first, and records the calls.
type store struct {
	n         int
	user      string
	remote    *secevent.Remote
	scope     tenant.Scope
	filter    secevents.Filter
	overview  secevents.OverviewQuery
	hostnames []identity.NodeID
}

func (s *store) Overview(_ context.Context, scope tenant.Scope, q secevents.OverviewQuery) (secevents.Overview, error) {
	s.scope, s.overview = scope, q
	return secevents.Overview{
		Totals: secevents.Totals{FailedLogins: 7},
		TopIPs: []secevents.Count{{Value: "81.2.69.1", Count: 3}, {Value: "10.0.0.1", Count: 2}},
	}, nil
}

// geo knows one address.
type geo struct{}

func (geo) Country(ip netip.Addr) string {
	if ip == netip.MustParseAddr("81.2.69.1") {
		return "GB"
	}
	return ""
}

func (geo) Notice() string { return "IP geolocation by DB-IP" }

func (s *store) Hostnames(_ context.Context, scope tenant.Scope, ids []identity.NodeID) (map[identity.NodeID]string, error) {
	s.scope, s.hostnames = scope, ids
	return map[identity.NodeID]string{node: "web-01"}, nil
}

func (s *store) List(_ context.Context, scope tenant.Scope, f secevents.Filter) ([]secevent.Event, error) {
	s.scope, s.filter = scope, f
	out := make([]secevent.Event, 0, min(s.n, f.Limit))
	for i := range min(s.n, f.Limit) {
		out = append(out, secevent.Event{ID: int64(1000 - i), Node: node, OccurredAt: t0.Add(-time.Duration(i) * time.Second), Kind: secevent.SSHLoginFailed, User: s.user, Remote: s.remote})
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

func TestStepFor_KeepsAtMost200Buckets(t *testing.T) {
	for r, want := range map[time.Duration]time.Duration{
		time.Hour:         time.Minute,
		200 * time.Minute: time.Minute,
		201 * time.Minute: 5 * time.Minute,
		// A part of a bucket is a bucket: 201 of them.
		200*time.Minute + 30*time.Second: 5 * time.Minute,
		24 * time.Hour:                   15 * time.Minute,
		7 * 24 * time.Hour:               time.Hour,
		30 * 24 * time.Hour:              6 * time.Hour,
		366 * 24 * time.Hour:             48 * time.Hour,
	} {
		if got := secevents.StepFor(r); got != want {
			t.Errorf("StepFor(%v) = %v, want %v", r, got, want)
		}
	}
}

func TestOverview_InTheScopeWithAStep(t *testing.T) {
	s := &store{}
	o, err := service(s).Overview(context.Background(), caller(role.Viewer, false), secevents.OverviewRequest{
		From: t0.Add(-24 * time.Hour), To: t0, Nodes: []identity.NodeID{node},
	})
	if err != nil || o.Totals.FailedLogins != 7 || o.Step != 15*time.Minute {
		t.Fatalf("Overview() = %+v, %v", o, err)
	}
	q := s.overview
	if !s.scope.AllowsGroup(groupX) || s.scope.AllGroups() || q.Step != 15*time.Minute || q.Top != 10 ||
		len(q.Nodes) != 1 || !q.From.Equal(t0.Add(-24*time.Hour)) || !q.To.Equal(t0) {
		t.Errorf("scope %+v, query %+v", s.scope, q)
	}
	_, err = service(&store{}, authz.WithViewerLogsDisabled(org)).Overview(context.Background(), caller(role.Viewer, true),
		secevents.OverviewRequest{From: t0.Add(-time.Hour), To: t0})
	if !errors.Is(err, access.ErrForbidden) {
		t.Errorf("viewer without logs: %v, want forbidden", err)
	}
	for name, r := range map[string]secevents.OverviewRequest{
		"to before from": {From: t0, To: t0.Add(-time.Hour)},
		"over a year":    {From: t0.Add(-367 * 24 * time.Hour), To: t0},
		"too many nodes": {From: t0.Add(-time.Hour), To: t0, Nodes: make([]identity.NodeID, 101)},
	} {
		if _, err := service(&store{}).Overview(context.Background(), caller(role.Admin, true), r); !errors.Is(err, secevent.ErrInvalid) {
			t.Errorf("%s: %v, want invalid", name, err)
		}
	}
}

func TestExport_WritesCSVWithHostnamesAndBounds(t *testing.T) {
	s := &store{n: 3, user: "=1+1"}
	req := request().Filter
	req.Before = &secevents.Cursor{At: t0, ID: 1}
	e, err := service(s).Export(context.Background(), caller(role.Viewer, false), req)
	if err != nil || e.Rows != 3 || e.Truncated {
		t.Fatalf("Export() = %+v, %v", e, err)
	}
	if e.FileName != "sermon-security-events-20261120T1100Z-20261120T1200Z.csv" {
		t.Errorf("file name %q", e.FileName)
	}
	if s.filter.Limit != secevents.MaxExportRows+1 || s.filter.Before != nil || len(s.hostnames) != 1 || !s.scope.AllowsGroup(groupX) {
		t.Errorf("filter %+v, hostnames of %v", s.filter, s.hostnames)
	}
	text := string(e.Content)
	if !strings.Contains(text, ",web-01,ssh.login.failed,'=1+1,") || strings.Count(text, "\r\n") != 4 {
		t.Errorf("content:\n%s", text)
	}

	many := &store{n: secevents.MaxExportRows + 5}
	if e, err := service(many).Export(context.Background(), caller(role.Viewer, false), request().Filter); err != nil ||
		e.Rows != secevents.MaxExportRows || !e.Truncated {
		t.Errorf("over the bound: %d rows, truncated %v, %v", e.Rows, e.Truncated, err)
	}
	if _, err := service(&store{}, authz.WithViewerLogsDisabled(org)).Export(context.Background(), caller(role.Viewer, true), request().Filter); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("viewer without logs: %v", err)
	}
	bad := request().Filter
	bad.Kinds = []secevent.Kind{"ssh.root"}
	if _, err := service(&store{}).Export(context.Background(), caller(role.Admin, true), bad); !errors.Is(err, secevent.ErrInvalid) {
		t.Errorf("bad filter: %v", err)
	}
}

// M3-18: with a country database the top addresses and the export carry the countries.
func TestGeo_CountriesOfTheTopAddressesAndTheExport(t *testing.T) {
	s := &store{n: 1}
	svc := service(s)
	req := secevents.OverviewRequest{From: t0.Add(-time.Hour), To: t0}
	o, err := svc.Overview(context.Background(), caller(role.Viewer, true), req)
	if err != nil || o.GeoNotice != "" || o.TopIPs[0].Country != "" || svc.Country(netip.MustParseAddr("81.2.69.1")) != "" {
		t.Fatalf("without GeoIP: %+v, %v", o, err)
	}
	svc.SetGeo(geo{})
	o, err = svc.Overview(context.Background(), caller(role.Viewer, true), req)
	if err != nil || o.GeoNotice != "IP geolocation by DB-IP" || o.TopIPs[0].Country != "GB" || o.TopIPs[1].Country != "" {
		t.Errorf("with GeoIP: %+v, %v", o, err)
	}
	if svc.Country(netip.Addr{}) != "" || svc.GeoNotice() != "IP geolocation by DB-IP" {
		t.Error("Country of no address or the notice")
	}
	s.remote = &secevent.Remote{IP: netip.MustParseAddr("81.2.69.1")}
	e, err := svc.Export(context.Background(), caller(role.Viewer, true), request().Filter)
	if err != nil || !strings.Contains(string(e.Content), ",GB\r\n") {
		t.Errorf("export:\n%s, %v", e.Content, err)
	}
}
