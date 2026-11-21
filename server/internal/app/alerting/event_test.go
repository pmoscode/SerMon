package alerting_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/alerting"
	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/secevent"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	app01   = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000003"))
	appNode = fleet.Node{ID: app01, Group: group, Hostname: "app-01"}
)

// fakeEvents holds the security events of each organization, like the store behind RLS.
type fakeEvents struct {
	mu      sync.Mutex
	byOrg   map[tenant.OrgID][]secevent.Event
	queries []alerting.EventQuery
}

func (f *fakeEvents) add(org tenant.OrgID, events ...secevent.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.byOrg == nil {
		f.byOrg = map[tenant.OrgID][]secevent.Event{}
	}
	for _, e := range events {
		e.ID = int64(len(f.byOrg[org]) + 1)
		f.byOrg[org] = append(f.byOrg[org], e)
	}
}

func (f *fakeEvents) Received(_ context.Context, scope tenant.Scope, q alerting.EventQuery) ([]secevent.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !scope.Valid() {
		return nil, errors.New("query without scope")
	}
	f.queries = append(f.queries, q)
	var out []secevent.Event
	for _, e := range f.byOrg[scope.OrgID()] {
		if e.Kind == q.Kind && contains(q.Nodes, e.Node) && !e.ReceivedAt.Before(q.Since) {
			out = append(out, e)
		}
	}
	slices.SortStableFunc(out, func(a, b secevent.Event) int { return b.ReceivedAt.Compare(a.ReceivedAt) })
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// ev is an event of a node that occurred ago before t0 and arrived at once.
func ev(kind secevent.Kind, node identity.NodeID, ago time.Duration, ip, user string) secevent.Event {
	e := secevent.Event{Node: node, Kind: kind, User: user, OccurredAt: t0.Add(-ago), ReceivedAt: t0.Add(-ago)}
	if ip != "" {
		e.Remote = &secevent.Remote{IP: netip.MustParseAddr(ip), Port: 50022}
	}
	return e
}

// burst is n events of a node spread evenly over span, the last one ago before t0.
func burst(kind secevent.Kind, node identity.NodeID, n int, span, ago time.Duration, ip, user string) []secevent.Event {
	out := make([]secevent.Event, n)
	for i := range out {
		offset := time.Duration(0)
		if n > 1 {
			offset = span * time.Duration(n-1-i) / time.Duration(n-1)
		}
		out[i] = ev(kind, node, ago+offset, ip, user)
	}
	return out
}

func eventEnv(f *fakeEvents, org tenant.OrgID, nodes ...fleet.Node) alerting.Env {
	scope, _ := tenant.ForAllGroups(org)
	return alerting.Env{Scope: scope, Nodes: nodes, Now: t0, Events: f}
}

// bruteforce is the default rule of KONZEPT §12.2: 10 / 50 failed logins of one IP in 5 min.
func bruteforce() alerting.EventRule {
	return alerting.EventRule{
		Kind: secevent.SSHLoginFailed, GroupBy: []string{"ip"}, Window: 5 * time.Minute, Warning: 10, Critical: 50,
		NotifyFields: []string{"ts", "user", "ip", "port"},
	}
}

func evaluateEvents(t *testing.T, rule alerting.EventRule, env alerting.Env) map[string]alert.Observation {
	t.Helper()
	if err := rule.Validate(); err != nil {
		t.Fatal(err)
	}
	results, err := rule.Evaluate(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	return byKey(t, results)
}

func ipDim(ip string) string { return metrics.Labels{"ip": ip}.Key() }

func TestEvent_BruteForceCountsPerNodeAndIP(t *testing.T) {
	f := &fakeEvents{}
	f.add(orgA, burst(secevent.SSHLoginFailed, web, 12, 2*time.Minute, 10*time.Second, "192.0.2.7", "root")...)
	f.add(orgA, burst(secevent.SSHLoginFailed, web, 55, 4*time.Minute, 10*time.Second, "198.51.100.9", "admin")...)
	f.add(orgA, burst(secevent.SSHLoginFailed, web, 3, time.Minute, 10*time.Second, "203.0.113.1", "bob")...)
	// The same IP on another node is another instance.
	f.add(orgA, burst(secevent.SSHLoginFailed, db01, 9, time.Minute, 10*time.Second, "192.0.2.7", "root")...)
	got := evaluateEvents(t, bruteforce(), eventEnv(f, orgA, webNode, dbNode))

	if o := got[web.String()+"|"+ipDim("192.0.2.7")]; o.Level != alert.LevelWarning || o.Value != 12 {
		t.Errorf("12 failures: %+v, want warning at 12", o)
	}
	if o := got[web.String()+"|"+ipDim("198.51.100.9")]; o.Level != alert.LevelCritical || o.Value != 55 {
		t.Errorf("55 failures: %+v, want critical at 55", o)
	}
	if o := got[web.String()+"|"+ipDim("203.0.113.1")]; o.Level != alert.LevelOK || !o.Cleared || o.Value != 3 {
		t.Errorf("3 failures: %+v, want ok", o)
	}
	if o := got[db01.String()+"|"+ipDim("192.0.2.7")]; o.Level != alert.LevelOK || o.Value != 9 {
		t.Errorf("9 failures on db: %+v, want ok (counted per node)", o)
	}
	o := got[web.String()+"|"+ipDim("198.51.100.9")]
	want := alert.Fields{"ts": "2026-11-20T11:59:50Z", "user": "admin", "ip": "198.51.100.9", "port": "50022"}
	if fmt.Sprint(o.Fields) != fmt.Sprint(want) {
		t.Errorf("fields = %v, want those of the latest event %v", o.Fields, want)
	}
	q := f.queries[0]
	if q.Kind != secevent.SSHLoginFailed || !q.Since.Equal(t0.Add(-5*time.Minute)) || len(q.Nodes) != 2 || q.Limit != alerting.MaxEventsRead+1 {
		t.Errorf("query = %+v, want failed logins received in the window", q)
	}
}

// The window is half open: ten events count together only within less than five minutes.
func TestEvent_WindowBoundary(t *testing.T) {
	for name, tc := range map[string]struct {
		span time.Duration
		want float64
	}{
		"just inside":  {5*time.Minute - time.Second, 10},
		"on the edge":  {5 * time.Minute, 9},
		"far too slow": {50 * time.Minute, 1},
	} {
		f := &fakeEvents{}
		events := burst(secevent.SSHLoginFailed, web, 10, tc.span, 0, "192.0.2.7", "root")
		for i := range events {
			events[i].ReceivedAt = t0 // a backlog arriving at once after an outage
		}
		f.add(orgA, events...)
		o := evaluateEvents(t, bruteforce(), eventEnv(f, orgA, webNode))[web.String()+"|"+ipDim("192.0.2.7")]
		if o.Value != tc.want {
			t.Errorf("%s: count %v, want %v", name, o.Value, tc.want)
		}
	}
}

// Events an agent delivers late (it was offline) count by when they occurred: a burst within
// the backlog fires on arrival, even though it lies hours back.
func TestEvent_LateBacklogCountsByOccurrence(t *testing.T) {
	f := &fakeEvents{}
	events := burst(secevent.SSHLoginFailed, web, 20, time.Minute, 3*time.Hour, "192.0.2.7", "root")
	for i := range events {
		events[i].ReceivedAt = t0.Add(-time.Minute)
	}
	f.add(orgA, events...)
	if o := evaluateEvents(t, bruteforce(), eventEnv(f, orgA, webNode))[web.String()+"|"+ipDim("192.0.2.7")]; o.Level != alert.LevelWarning || o.Value != 20 {
		t.Errorf("backlog: %+v, want warning at 20", o)
	}
	// Received before the window: done.
	f = &fakeEvents{}
	f.add(orgA, burst(secevent.SSHLoginFailed, web, 20, time.Minute, 6*time.Minute, "192.0.2.7", "root")...)
	if got := evaluateEvents(t, bruteforce(), eventEnv(f, orgA, webNode)); len(got) != 0 {
		t.Errorf("old events: %+v, want nothing", got)
	}
}

func TestEvent_IPv6CountsByPrefix(t *testing.T) {
	f := &fakeEvents{}
	for i := range 12 {
		f.add(orgA, ev(secevent.SSHLoginFailed, web, time.Duration(i)*time.Second, fmt.Sprintf("2001:db8:1:2::%x", i+1), "root"))
	}
	got := evaluateEvents(t, bruteforce(), eventEnv(f, orgA, webNode))
	if o := got[web.String()+"|"+ipDim("2001:db8:1:2::/64")]; o.Level != alert.LevelWarning || o.Value != 12 || o.Fields["ip"] != "2001:db8:1:2::1" {
		t.Errorf("rotating addresses in a /64: %+v (all %v)", o, got)
	}
}

func TestEvent_GroupByUserAndMatch(t *testing.T) {
	f := &fakeEvents{}
	f.add(orgA, ev(secevent.SSHLoginSucceeded, web, time.Minute, "192.0.2.7", "root"))
	f.add(orgA, ev(secevent.SSHLoginSucceeded, web, 2*time.Minute, "192.0.2.8", "alice"))
	rootLogin := alerting.EventRule{
		Kind: secevent.SSHLoginSucceeded, Match: map[string]string{"user": "root"}, GroupBy: []string{"ip"},
		Window: 15 * time.Minute, Warning: 1, NotifyFields: []string{"user", "ip"},
	}
	got := evaluateEvents(t, rootLogin, eventEnv(f, orgA, webNode))
	if len(got) != 1 || got[web.String()+"|"+ipDim("192.0.2.7")].Level != alert.LevelWarning {
		t.Errorf("root login: %+v, want one warning for root's IP", got)
	}

	sudo := alerting.EventRule{Kind: secevent.SudoAuthFailed, GroupBy: []string{"user"}, Window: 10 * time.Minute, Warning: 3, Critical: 10}
	f = &fakeEvents{}
	f.add(orgA, burst(secevent.SudoAuthFailed, web, 4, time.Minute, 0, "", "alice")...)
	f.add(orgA, burst(secevent.SudoAuthFailed, web, 2, time.Minute, 0, "", "bob")...)
	got = evaluateEvents(t, sudo, eventEnv(f, orgA, webNode))
	if o := got[web.String()+"|"+metrics.Labels{"user": "alice"}.Key()]; o.Level != alert.LevelWarning || o.Value != 4 {
		t.Errorf("alice: %+v", o)
	}
	if o := got[web.String()+"|"+metrics.Labels{"user": "bob"}.Key()]; o.Level != alert.LevelOK {
		t.Errorf("bob: %+v", o)
	}
}

// Without groupBy one instance per node counts every event (OOM kills, reboots).
func TestEvent_WithoutGroupingOneInstancePerNode(t *testing.T) {
	f := &fakeEvents{}
	oom := ev(secevent.OOMKill, web, time.Minute, "", "")
	oom.Details = secevent.Details{PID: 4711, Process: "java"}
	f.add(orgA, oom, ev(secevent.OOMKill, web, 2*time.Minute, "", ""))
	rule := alerting.EventRule{Kind: secevent.OOMKill, Window: 15 * time.Minute, Critical: 1, NotifyFields: []string{"ts", "pid", "process"}}
	got := evaluateEvents(t, rule, eventEnv(f, orgA, webNode, dbNode))
	if o := got[web.String()+"|"]; len(got) != 1 || o.Level != alert.LevelCritical || o.Value != 2 || o.Fields["process"] != "java" {
		t.Errorf("oom: %+v", got)
	}
}

// Fleet correlation: the same IP fails on at least three nodes of the organization.
func correlation() alerting.EventRule {
	return alerting.EventRule{
		Kind: secevent.SSHLoginFailed, GroupBy: []string{"ip"}, Count: alerting.CountNodes, Window: 15 * time.Minute, Warning: 3,
		NotifyFields: []string{"user", "ip"},
	}
}

func TestEvent_FleetCorrelationOverThreeNodes(t *testing.T) {
	f := &fakeEvents{}
	f.add(orgA,
		ev(secevent.SSHLoginFailed, web, time.Minute, "192.0.2.7", "root"),
		ev(secevent.SSHLoginFailed, web, 2*time.Minute, "192.0.2.7", "root"),
		ev(secevent.SSHLoginFailed, db01, 3*time.Minute, "192.0.2.7", "admin"),
		ev(secevent.SSHLoginFailed, app01, 4*time.Minute, "192.0.2.7", "oracle"),
		// Two nodes only:
		ev(secevent.SSHLoginFailed, web, time.Minute, "198.51.100.9", "root"),
		ev(secevent.SSHLoginFailed, db01, time.Minute, "198.51.100.9", "root"),
	)
	got := evaluateEvents(t, correlation(), eventEnv(f, orgA, webNode, dbNode, appNode))
	for node, user := range map[identity.NodeID]string{web: "root", db01: "admin", app01: "oracle"} {
		if o := got[node.String()+"|"+ipDim("192.0.2.7")]; o.Level != alert.LevelWarning || o.Value != 3 || o.Fields["user"] != user {
			t.Errorf("node %v: %+v, want warning at 3 nodes with its own latest user %s", node, o, user)
		}
	}
	if o := got[web.String()+"|"+ipDim("198.51.100.9")]; o.Level != alert.LevelOK || o.Value != 2 {
		t.Errorf("two nodes: %+v, want ok", o)
	}
}

func TestEvent_FleetCorrelationNeedsACommonWindow(t *testing.T) {
	f := &fakeEvents{}
	f.add(orgA,
		ev(secevent.SSHLoginFailed, web, 0, "192.0.2.7", "root"),
		ev(secevent.SSHLoginFailed, db01, 8*time.Minute, "192.0.2.7", "root"),
		ev(secevent.SSHLoginFailed, app01, 16*time.Minute, "192.0.2.7", "root"),
	)
	for i := range f.byOrg[orgA] {
		f.byOrg[orgA][i].ReceivedAt = t0
	}
	if o := evaluateEvents(t, correlation(), eventEnv(f, orgA, webNode, dbNode, appNode))[web.String()+"|"+ipDim("192.0.2.7")]; o.Value != 2 {
		t.Errorf("three nodes over 16 minutes: %+v, want at most two within 15 minutes", o)
	}
}

// Correlation never crosses organizations: each evaluation sees its organization's events only.
func TestEvent_FleetCorrelationStaysWithinTheOrganization(t *testing.T) {
	f := &fakeEvents{}
	f.add(orgA, ev(secevent.SSHLoginFailed, web, time.Minute, "192.0.2.7", "root"), ev(secevent.SSHLoginFailed, db01, time.Minute, "192.0.2.7", "root"))
	f.add(orgB, ev(secevent.SSHLoginFailed, app01, time.Minute, "192.0.2.7", "root"))
	for org, nodes := range map[tenant.OrgID][]fleet.Node{orgA: {webNode, dbNode}, orgB: {appNode}} {
		for k, o := range evaluateEvents(t, correlation(), eventEnv(f, org, nodes...)) {
			if o.Level != alert.LevelOK {
				t.Errorf("org %v, %s: %+v, want no correlation across organizations", org, k, o)
			}
		}
	}
}

// Successful login after failed attempts from the same IP on the node.
func successAfterFailures() alerting.EventRule {
	return alerting.EventRule{
		Kind: secevent.SSHLoginSucceeded, GroupBy: []string{"ip"}, Window: 15 * time.Minute, Critical: 1,
		After:        &alerting.EventCondition{Kind: secevent.SSHLoginFailed, Count: 5, Window: time.Hour},
		NotifyFields: []string{"ts", "user", "ip", "port", "method"},
	}
}

func TestEvent_SuccessAfterFailures(t *testing.T) {
	f := &fakeEvents{}
	// 192.0.2.7: six failures, then in.
	f.add(orgA, burst(secevent.SSHLoginFailed, web, 6, 20*time.Minute, 5*time.Minute, "192.0.2.7", "root")...)
	f.add(orgA, ev(secevent.SSHLoginSucceeded, web, 2*time.Minute, "192.0.2.7", "root"))
	// 198.51.100.9: four failures only.
	f.add(orgA, burst(secevent.SSHLoginFailed, web, 4, 10*time.Minute, 5*time.Minute, "198.51.100.9", "alice")...)
	f.add(orgA, ev(secevent.SSHLoginSucceeded, web, time.Minute, "198.51.100.9", "alice"))
	// 203.0.113.1: the failures came from another IP; and failures on another node don't count.
	f.add(orgA, ev(secevent.SSHLoginSucceeded, web, time.Minute, "203.0.113.1", "bob"))
	f.add(orgA, burst(secevent.SSHLoginFailed, db01, 10, 10*time.Minute, 5*time.Minute, "203.0.113.1", "bob")...)
	// 192.0.2.99: failures after the success don't make it suspicious.
	f.add(orgA, ev(secevent.SSHLoginSucceeded, web, 10*time.Minute, "192.0.2.99", "bob"))
	f.add(orgA, burst(secevent.SSHLoginFailed, web, 10, 5*time.Minute, time.Minute, "192.0.2.99", "bob")...)

	got := evaluateEvents(t, successAfterFailures(), eventEnv(f, orgA, webNode, dbNode))
	if o := got[web.String()+"|"+ipDim("192.0.2.7")]; o.Level != alert.LevelCritical || o.Fields["user"] != "root" {
		t.Errorf("after six failures: %+v, want critical", o)
	}
	for _, ip := range []string{"198.51.100.9", "203.0.113.1", "192.0.2.99"} {
		if o, ok := got[web.String()+"|"+ipDim(ip)]; ok && o.Level != alert.LevelOK {
			t.Errorf("%s: %+v, want no alert", ip, o)
		}
	}
	var failedSince time.Time
	for _, q := range f.queries {
		if q.Kind == secevent.SSHLoginFailed {
			failedSince = q.Since
		}
	}
	if !failedSince.Equal(t0.Add(-75 * time.Minute)) {
		t.Errorf("failures read since %v, want window + the condition's window", failedSince)
	}
}

// Instances still open but without events in the window resolve: no events is a count of 0.
func TestEvent_OpenInstancesWithoutEventsAreZero(t *testing.T) {
	f := &fakeEvents{}
	env := eventEnv(f, orgA, webNode)
	env.Open = []alert.Key{{Rule: "security.ssh.bruteforce", Node: web, Dimension: ipDim("192.0.2.7")}}
	got := evaluateEvents(t, bruteforce(), env)
	if o := got[web.String()+"|"+ipDim("192.0.2.7")]; len(got) != 1 || o.NoData || o.Level != alert.LevelOK || !o.Cleared || o.Value != 0 {
		t.Errorf("open instance: %+v, want a cleared observation", got)
	}
}

// A flood beyond what a round reads keeps the open instances as they are instead of resolving
// them on a partial count, and caps the instances.
func TestEvent_FloodIsBounded(t *testing.T) {
	f := &fakeEvents{}
	events := make([]secevent.Event, 0, alerting.MaxEventsRead+10)
	for i := range alerting.MaxEventsRead + 10 {
		ip := netip.AddrFrom4([4]byte{192, 0, 2, byte(i % 250)})
		if i%2 == 0 {
			ip = netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)})
		}
		e := ev(secevent.SSHLoginFailed, web, time.Duration(i%240)*time.Second, "192.0.2.1", "root")
		e.Remote.IP = ip
		events = append(events, e)
	}
	f.add(orgA, events...)
	env := eventEnv(f, orgA, webNode)
	env.Open = []alert.Key{{Rule: "security.ssh.bruteforce", Node: web, Dimension: ipDim("203.0.113.1")}}
	results, err := bruteforce().Evaluate(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) > alerting.MaxEventInstances+1 {
		t.Errorf("%d results, want at most %d (+ the open one)", len(results), alerting.MaxEventInstances)
	}
	got := byKey(t, results)
	if o := got[web.String()+"|"+ipDim("203.0.113.1")]; !o.NoData {
		t.Errorf("open instance during a flood: %+v, want no data", o)
	}
	for k, o := range got {
		if strings.Contains(k, "ip=192.0.2.") && o.Level < alert.LevelWarning {
			t.Errorf("%s: %+v – the firing ones must survive the cap", k, o)
		}
	}
}

func TestEvent_ValidateRejects(t *testing.T) {
	ok := bruteforce()
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*alerting.EventRule){
		"unknown kind":            func(r *alerting.EventRule) { r.Kind = "ssh.login.whatever" },
		"no level":                func(r *alerting.EventRule) { r.Warning, r.Critical = 0, 0 },
		"critical below warning":  func(r *alerting.EventRule) { r.Critical = 5 },
		"negative":                func(r *alerting.EventRule) { r.Warning = -1 },
		"no window":               func(r *alerting.EventRule) { r.Window = 0 },
		"window over a day":       func(r *alerting.EventRule) { r.Window = 25 * time.Hour },
		"group by a missing":      func(r *alerting.EventRule) { r.GroupBy = []string{"tty"} },
		"group twice":             func(r *alerting.EventRule) { r.GroupBy = []string{"ip", "ip"} },
		"notify an unknown field": func(r *alerting.EventRule) { r.NotifyFields = []string{"uid"} },
		"notify twice":            func(r *alerting.EventRule) { r.NotifyFields = []string{"ip", "ip"} },
		"match an unknown field":  func(r *alerting.EventRule) { r.Match = map[string]string{"process": "java"} },
		"match on the time":       func(r *alerting.EventRule) { r.Match = map[string]string{"ts": "x"} },
		"match empty":             func(r *alerting.EventRule) { r.Match = map[string]string{"user": ""} },
		"nodes without grouping":  func(r *alerting.EventRule) { r.Count, r.GroupBy = alerting.CountNodes, nil },
		"after unknown kind": func(r *alerting.EventRule) {
			r.After = &alerting.EventCondition{Kind: "x", Count: 1, Window: time.Hour}
		},
		"after without count": func(r *alerting.EventRule) {
			r.After = &alerting.EventCondition{Kind: secevent.SSHLoginFailed, Window: time.Hour}
		},
		"after without window": func(r *alerting.EventRule) {
			r.After = &alerting.EventCondition{Kind: secevent.SSHLoginFailed, Count: 3}
		},
		"after lacks the group": func(r *alerting.EventRule) {
			r.After = &alerting.EventCondition{Kind: secevent.SudoAuthFailed, Count: 3, Window: time.Hour}
		},
	} {
		r := bruteforce()
		change(&r)
		if err := r.Validate(); !errors.Is(err, alert.ErrInvalidRule) {
			t.Errorf("%s: Validate() = %v, want ErrInvalidRule", name, err)
		}
	}
}

// Through the engine: a brute force fires at once, carries the fields into the history and
// resolves once its events age out of the window.
func TestEngine_EventRuleFiresAndResolves(t *testing.T) {
	store, c, f := newMemStore(orgA), &clock{now: t0}, &fakeEvents{}
	f.add(orgA, burst(secevent.SSHLoginFailed, web, 12, time.Minute, 0, "192.0.2.7", "root")...)
	const id alert.RuleID = "security.ssh.bruteforce"
	timing := alert.Timing{Flapping: alert.DefaultFlapping}
	source := rules{orgA: {{ID: id, Timing: timing, Evaluator: bruteforce()}, {ID: "security.ssh.distributed", Timing: timing, Evaluator: correlation()}}}
	e := alerting.NewEngine(alerting.Deps{
		Store: store, Rules: source, Nodes: directory{}, Events: f, Now: c.Now, Holder: "a",
		Logger: slog.New(slog.DiscardHandler),
	})
	e.Round(context.Background())
	key := alert.Key{Rule: id, Node: web, Dimension: ipDim("192.0.2.7")}
	i, ok := store.instance(orgA, key)
	if !ok || i.State != alert.StateWarning || i.Fields["user"] != "root" {
		t.Fatalf("after the burst: %+v, want warning with the user", i)
	}
	if h := store.history[orgA]; len(h) != 1 || h[0].Transition.Fields["ip"] != "192.0.2.7" {
		t.Errorf("history = %+v, want the fields", h)
	}
	// Both rules read failed logins: one query per window and round.
	if len(f.queries) != 2 {
		t.Errorf("%d queries, want one per window", len(f.queries))
	}
	// The count falls below 10 as the burst ages out of the window; then the instance is quiet.
	rounds(t, e, c, 30)
	if i, _ := store.instance(orgA, key); i.State != alert.StateOK {
		t.Errorf("after the window: %+v, want ok (kept for its flapping history)", i)
	}
	h := store.history[orgA]
	if len(h) != 3 || h[1].Transition.To != alert.StateResolved || h[1].Transition.At.Sub(t0) > 5*time.Minute || h[2].Transition.To != alert.StateOK {
		t.Errorf("history = %+v, want warning, resolved within five minutes, ok", h)
	}
}

func TestEngine_EventReadsAreCachedPerRoundAndTruncationsCounted(t *testing.T) {
	store, c, f := newMemStore(orgA), &clock{now: t0}, &fakeEvents{}
	flood := make([]secevent.Event, alerting.MaxEventsRead+1)
	for i := range flood {
		flood[i] = ev(secevent.SSHLoginFailed, web, time.Second, "192.0.2.7", "root")
	}
	f.add(orgA, flood...)
	timing := alert.Timing{Flapping: alert.DefaultFlapping}
	source := rules{orgA: {{ID: "security.ssh.bruteforce", Timing: timing, Evaluator: bruteforce()}, {ID: "security.ssh.bruteforce_too", Timing: timing, Evaluator: bruteforce()}}}
	e := alerting.NewEngine(alerting.Deps{
		Store: store, Rules: source, Nodes: directory{}, Events: f, Now: c.Now, Holder: "a",
		Logger: slog.New(slog.DiscardHandler),
	})
	e.Round(context.Background())
	if len(f.queries) != 1 {
		t.Errorf("%d queries for two rules on the same events, want 1", len(f.queries))
	}
	if n := e.Stats().EventReadsTruncated.Load(); n != 2 {
		t.Errorf("%d truncated reads, want 2 (one per rule)", n)
	}
	rounds(t, e, c, 1)
	if len(f.queries) != 2 {
		t.Errorf("%d queries after two rounds, want a fresh read per round", len(f.queries))
	}
}

// Field values are what an attacker chose (user names, commands): notifications get them without
// control characters and bounded.
func TestEvent_FieldsAreSanitized(t *testing.T) {
	f := &fakeEvents{}
	e := ev(secevent.SSHLoginFailed, web, 0, "192.0.2.7", "root\r\nX-Injected: 1\x1b[31m")
	e.Details.Method = strings.Repeat("ä", 200)
	f.add(orgA, e)
	rule := bruteforce()
	rule.Warning, rule.Critical = 1, 0
	rule.NotifyFields = []string{"user", "method"}
	o := evaluateEvents(t, rule, eventEnv(f, orgA, webNode))[web.String()+"|"+ipDim("192.0.2.7")]
	if got := o.Fields["user"]; got != "root��X-Injected: 1�[31m" {
		t.Errorf("user = %q, want the control characters replaced", got)
	}
	if got := o.Fields["method"]; len(got) > alerting.MaxFieldBytes || !strings.HasPrefix(got, "ää") || !utf8.ValidString(got) {
		t.Errorf("method = %q (%d bytes), want at most %d bytes of valid UTF-8", got, len(got), alerting.MaxFieldBytes)
	}
}

// A single event (OOM kill) fires for the window; once no event is left in it, the engine hands
// the open instance to the rule, which reports zero, and the alert resolves.
func TestEngine_EventAlertResolvesWhenTheWindowIsEmpty(t *testing.T) {
	store, c, f := newMemStore(orgA), &clock{now: t0}, &fakeEvents{}
	f.add(orgA, ev(secevent.OOMKill, web, 0, "", ""))
	const id alert.RuleID = "system.memory.oom_kill"
	rule := alerting.EventRule{Kind: secevent.OOMKill, Window: 15 * time.Minute, Critical: 1}
	e := alerting.NewEngine(alerting.Deps{
		Store: store, Rules: rules{orgA: {{ID: id, Timing: alert.Timing{Flapping: alert.DefaultFlapping}, Evaluator: rule}}},
		Nodes: directory{}, Events: f, Now: c.Now, Holder: "a", Logger: slog.New(slog.DiscardHandler),
	})
	e.Round(context.Background())
	key := alert.Key{Rule: id, Node: web}
	if i, _ := store.instance(orgA, key); i.State != alert.StateCritical {
		t.Fatalf("after the kill: %+v, want critical", i)
	}
	rounds(t, e, c, 6*14)
	if i, _ := store.instance(orgA, key); i.State != alert.StateCritical {
		t.Fatalf("within the window: %+v, want critical", i)
	}
	rounds(t, e, c, 7)
	if i, _ := store.instance(orgA, key); i.State.Firing() {
		t.Errorf("after the window: %+v, want resolved", i)
	}
}

// The dimension of an instance grouped by user is bounded and free of control characters too.
func TestEvent_DimensionsAreSanitized(t *testing.T) {
	f := &fakeEvents{}
	f.add(orgA, ev(secevent.SudoAuthFailed, web, 0, "", "eve\x1fuser=root\n"))
	rule := alerting.EventRule{Kind: secevent.SudoAuthFailed, GroupBy: []string{"user"}, Window: 10 * time.Minute, Warning: 1}
	got := evaluateEvents(t, rule, eventEnv(f, orgA, webNode))
	want := web.String() + "|" + metrics.Labels{"user": "eve�user=root�"}.Key()
	if _, ok := got[want]; len(got) != 1 || !ok {
		t.Errorf("results %v, want the dimension %q", got, want)
	}
}

// Events from classic log files (ADR 0025): one unverified event among the counted ones marks the
// alert, whatever notifyFields say – a forged line could have raised the count.
func TestEvent_UnverifiedEventsMarkTheAlert(t *testing.T) {
	f := &fakeEvents{}
	verified := burst(secevent.SSHLoginFailed, web, 11, 2*time.Minute, 10*time.Second, "192.0.2.7", "root")
	mixed := burst(secevent.SSHLoginFailed, web, 11, 2*time.Minute, 10*time.Second, "192.0.2.8", "root")
	mixed[3].Unverified = true
	f.add(orgA, verified...)
	f.add(orgA, mixed...)
	got := evaluateEvents(t, bruteforce(), eventEnv(f, orgA, webNode))
	if o := got[web.String()+"|"+ipDim("192.0.2.7")]; o.Fields[secevent.FieldSource] != "" {
		t.Errorf("verified only: %v, want no source field", o.Fields)
	}
	if o := got[web.String()+"|"+ipDim("192.0.2.8")]; o.Fields[secevent.FieldSource] != secevent.SourceUnverified || o.Fields["user"] != "root" {
		t.Errorf("one unverified: %v, want source=unverified next to the notify fields", o.Fields)
	}

	// Without notifyFields, and per node in a fleet correlation.
	g := &fakeEvents{}
	oom := ev(secevent.OOMKill, web, time.Minute, "", "")
	oom.Unverified = true
	g.add(orgA, oom)
	rule := alerting.EventRule{Kind: secevent.OOMKill, Window: 15 * time.Minute, Critical: 1}
	if o := evaluateEvents(t, rule, eventEnv(g, orgA, webNode))[web.String()+"|"]; o.Fields[secevent.FieldSource] != secevent.SourceUnverified {
		t.Errorf("oom without notify fields: %v", o.Fields)
	}
	h := &fakeEvents{}
	forged := ev(secevent.SSHLoginFailed, app01, 3*time.Minute, "192.0.2.7", "x")
	forged.Unverified = true
	h.add(orgA, ev(secevent.SSHLoginFailed, web, time.Minute, "192.0.2.7", "root"), ev(secevent.SSHLoginFailed, db01, 2*time.Minute, "192.0.2.7", "root"), forged)
	for node, o := range evaluateEvents(t, correlation(), eventEnv(h, orgA, webNode, dbNode, appNode)) {
		if o.Level != alert.LevelWarning || o.Fields[secevent.FieldSource] != secevent.SourceUnverified {
			t.Errorf("correlation %s: %+v, want every node marked: one node's events may be forged", node, o)
		}
	}
}
