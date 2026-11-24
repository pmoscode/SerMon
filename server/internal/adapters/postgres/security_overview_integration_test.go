//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/app/alerting"
	"github.com/pmoscode/SerMon/server/internal/app/secevents"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/secevent"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// ownerTenantExec runs sql as the owner in a transaction of the organization (row-level
// security binds the owner as well).
func ownerTenantExec(t *testing.T, org uuid.UUID, sql string, args ...any) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, setup(t).db.OwnerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT set_config('sermon.org_id', $1, true)", org.String()); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, sql, args...)
		return err
	})
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func login(node identity.NodeID, at time.Time, user string, remote secevent.Remote) secevent.Event {
	return secevent.Event{Node: node, OccurredAt: at, Kind: secevent.SSHLoginSucceeded, User: user, Remote: &remote, Details: secevent.Details{Method: "publickey"}}
}

func from(ip string) secevent.Remote {
	return secevent.Remote{IP: netip.MustParseAddr(ip), Port: 50022}
}

// newMarks lists user@source:new of the stored logins, oldest first.
func (f eventFixture) newMarks(t *testing.T, scope tenant.Scope, node identity.NodeID) string {
	t.Helper()
	events := f.list(t, scope, secevents.Filter{Kinds: []secevent.Kind{secevent.SSHLoginSucceeded}, Nodes: []identity.NodeID{node}})
	var out []string
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		source, _ := e.LoginSource()
		if source == "" && e.Remote == nil {
			source = "console"
		}
		out = append(out, fmt.Sprintf("%s@%s:%v", e.User, source, e.NewSource))
	}
	return strings.Join(out, ",")
}

// "New" is the first login of a user from a source on a node, exactly once: IPv6 by its /64,
// a resent batch does not count again, another node and another organization learn their own.
func TestSecurityEventStore_MarksTheFirstLoginFromASource(t *testing.T) {
	f := newEventFixture(t)
	ctx := context.Background()
	org := tenant.OrgID(f.org)
	at := func(minutes int) time.Time { return eventsNow.Add(time.Duration(minutes-60) * time.Minute) }
	first := secevents.Batch{Seq: 1, Events: []secevent.Event{
		login(f.node, at(1), "alice", from("192.0.2.7")),
		login(f.node, at(2), "alice", from("192.0.2.7")),
		login(f.node, at(3), "alice", from("192.0.2.8")),
		login(f.node, at(4), "bob", from("192.0.2.7")),
		login(f.node, at(5), "alice", from("2001:db8:1:2::1")),
		login(f.node, at(6), "alice", from("2001:db8:1:2::99")),
		login(f.node, at(7), "alice", secevent.Remote{Host: "jump.example"}),
		{Node: f.node, OccurredAt: at(8), Kind: secevent.SSHLoginSucceeded, User: "alice"},
		failedLogin(f.node, at(9), "carol", "192.0.2.9"),
	}}
	if _, err := f.store.Write(ctx, org, f.node, []secevents.Batch{first}); err != nil {
		t.Fatal(err)
	}
	// After a reconnect the agent sends batch 1 again, then 2.
	second := secevents.Batch{Seq: 2, Events: []secevent.Event{
		login(f.node, at(10), "alice", from("192.0.2.8")),
		login(f.node, at(11), "carol", from("192.0.2.9")),
	}}
	if _, err := f.store.Write(ctx, org, f.node, []secevents.Batch{first, second}); err != nil {
		t.Fatal(err)
	}
	all := scopeFor(t, f.org)
	want := "alice@192.0.2.7:true,alice@192.0.2.7:false,alice@192.0.2.8:true,bob@192.0.2.7:true," +
		"alice@2001:db8:1:2::/64:true,alice@2001:db8:1:2::/64:false,alice@jump.example:true,alice@console:false," +
		"alice@192.0.2.8:false,carol@192.0.2.9:true"
	if got := f.newMarks(t, all, f.node); got != want {
		t.Errorf("marks\n%s\nwant\n%s", got, want)
	}
	newOnly := f.list(t, all, secevents.Filter{NewSourcesOnly: true})
	if len(newOnly) != 6 {
		t.Errorf("%d logins from new sources, want 6", len(newOnly))
	}

	// The peer node and another organization know none of these yet.
	if _, err := f.store.Write(ctx, org, f.peer, []secevents.Batch{{Seq: 1, Events: []secevent.Event{login(f.peer, at(12), "alice", from("192.0.2.7"))}}}); err != nil {
		t.Fatal(err)
	}
	if got := f.newMarks(t, all, f.peer); got != "alice@192.0.2.7:true" {
		t.Errorf("peer: %s", got)
	}
	other := newEventFixture(t)
	if _, err := other.store.Write(ctx, tenant.OrgID(other.org), other.node, []secevents.Batch{{Seq: 1, Events: []secevent.Event{login(other.node, at(13), "alice", from("192.0.2.7"))}}}); err != nil {
		t.Fatal(err)
	}
	if got := other.newMarks(t, scopeFor(t, other.org), other.node); got != "alice@192.0.2.7:true" {
		t.Errorf("another organization: %s", got)
	}

	// The alert engine reads the mark.
	received, err := f.store.Received(ctx, all, alerting.EventQuery{Kind: secevent.SSHLoginSucceeded, Nodes: []identity.NodeID{f.node}, Since: eventsNow.Add(-2 * time.Hour), Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	marked := 0
	for _, e := range received {
		if e.NewSource {
			marked++
		}
	}
	if marked != 6 {
		t.Errorf("Received: %d marked, want 6", marked)
	}
}

// A source unused for a year is new again; beyond the bound of a node an unknown source is
// neither recorded nor marked.
func TestSecurityEventStore_ForgetsOldSourcesAndBoundsThem(t *testing.T) {
	f := newEventFixture(t)
	ctx := context.Background()
	org := tenant.OrgID(f.org)
	ownerTenantExec(t, f.org, `INSERT INTO known_login_sources (org_id, node_id, user_name, source, first_seen, last_seen)
		VALUES ($1, $2, 'alice', '192.0.2.7', now() - interval '800 days', now() - interval '400 days'),
		       ($1, $2, 'alice', '192.0.2.8', now() - interval '800 days', now() - interval '300 days')`, f.org, uuid.UUID(f.node))
	batch := secevents.Batch{Seq: 1, Events: []secevent.Event{
		login(f.node, eventsNow.Add(-2*time.Minute), "alice", from("192.0.2.7")),
		login(f.node, eventsNow.Add(-time.Minute), "alice", from("192.0.2.8")),
	}}
	if _, err := f.store.Write(ctx, org, f.node, []secevents.Batch{batch}); err != nil {
		t.Fatal(err)
	}
	if got := f.newMarks(t, scopeFor(t, f.org), f.node); got != "alice@192.0.2.7:true,alice@192.0.2.8:false" {
		t.Errorf("after a year: %s", got)
	}

	ownerTenantExec(t, f.org, `INSERT INTO known_login_sources (org_id, node_id, user_name, source, first_seen, last_seen)
		SELECT $1, $2, 'flood', 'host-' || i, now(), now() FROM generate_series(1, $3::int - 1) i`,
		f.org, uuid.UUID(f.peer), postgres.MaxLoginSources)
	full := secevents.Batch{Seq: 1, Events: []secevent.Event{
		login(f.peer, eventsNow.Add(-3*time.Minute), "alice", from("192.0.2.7")),
		login(f.peer, eventsNow.Add(-2*time.Minute), "alice", from("192.0.2.8")),
		login(f.peer, eventsNow.Add(-time.Minute), "alice", from("192.0.2.7")),
		login(f.peer, eventsNow, "flood", secevent.Remote{Host: "host-1"}),
	}}
	if _, err := f.store.Write(ctx, org, f.peer, []secevents.Batch{full}); err != nil {
		t.Fatal(err)
	}
	if got := f.newMarks(t, scopeFor(t, f.org), f.peer); got != "alice@192.0.2.7:true,alice@192.0.2.8:false,alice@192.0.2.7:false,flood@host-1:false" {
		t.Errorf("at the bound: %s", got)
	}
}

// The evaluations of a range (M3-16) within the scope: failed logins per node and bucket, the
// top addresses and users, the totals; a group scope and a node filter narrow them.
func TestSecurityEventStore_Overview(t *testing.T) {
	f := newEventFixture(t)
	ctx := context.Background()
	org := tenant.OrgID(f.org)
	base := eventsNow.Truncate(time.Hour).Add(-2 * time.Hour)
	invalid := secevent.Event{Node: f.node, OccurredAt: base.Add(70 * time.Minute), Kind: secevent.SSHInvalidUser, User: "", Remote: &secevent.Remote{IP: netip.MustParseAddr("192.0.2.66")}}
	forged := failedLogin(f.node, base.Add(80*time.Minute), "root", "192.0.2.66")
	forged.Unverified = true
	nodeEvents := []secevent.Event{
		failedLogin(f.node, base.Add(time.Minute), "root", "192.0.2.66"),
		failedLogin(f.node, base.Add(2*time.Minute), "root", "192.0.2.66"),
		failedLogin(f.node, base.Add(3*time.Minute), "admin", "192.0.2.5"),
		invalid, forged,
		login(f.node, base.Add(90*time.Minute), "root", from("192.0.2.10")),
		login(f.node, base.Add(91*time.Minute), "root", from("192.0.2.10")),
		{Node: f.node, OccurredAt: base.Add(92 * time.Minute), Kind: secevent.SudoAuthFailed, User: "alice"},
		{Node: f.node, OccurredAt: base.Add(93 * time.Minute), Kind: secevent.SudoNotAllowed, User: "bob"},
		{Node: f.node, OccurredAt: base.Add(94 * time.Minute), Kind: secevent.SudoCommand, User: "alice"},
		// Outside the range.
		failedLogin(f.node, base.Add(-time.Minute), "root", "192.0.2.66"),
	}
	if _, err := f.store.Write(ctx, org, f.node, []secevents.Batch{{Seq: 1, Events: nodeEvents}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Write(ctx, org, f.peer, []secevents.Batch{{Seq: 1, Events: []secevent.Event{
		failedLogin(f.peer, base.Add(5*time.Minute), "root", "192.0.2.66"),
		login(f.peer, base.Add(6*time.Minute), "alice", from("192.0.2.10")),
	}}}); err != nil {
		t.Fatal(err)
	}
	q := secevents.OverviewQuery{From: base, To: base.Add(2 * time.Hour), Step: time.Hour, Top: 10}
	o, err := f.store.Overview(ctx, scopeFor(t, f.org), q)
	if err != nil {
		t.Fatal(err)
	}
	series := map[identity.NodeID]string{}
	for _, s := range o.FailedLogins {
		var points []string
		for _, p := range s.Points {
			points = append(points, fmt.Sprintf("+%v=%d", p.At.Sub(base), p.Count))
		}
		series[s.Node] = strings.Join(points, " ")
	}
	if series[f.node] != "+0s=3 +1h0m0s=2" || series[f.peer] != "+0s=1" || len(series) != 2 {
		t.Errorf("failed logins = %v", series)
	}
	top := func(cs []secevents.Count) string {
		var out []string
		for _, c := range cs {
			out = append(out, fmt.Sprintf("%s=%d/%d/%d", c.Value, c.Count, c.Nodes, c.Unverified))
		}
		return strings.Join(out, ",")
	}
	if got := top(o.TopIPs); got != "192.0.2.66=5/2/1,192.0.2.5=1/1/0" {
		t.Errorf("top IPs = %s", got)
	}
	if got := top(o.TopUsers); got != "root=4/2/1,=1/1/0,admin=1/1/0" {
		t.Errorf("top users = %s", got)
	}
	if !o.TopIPs[0].Last.Equal(base.Add(80 * time.Minute)) {
		t.Errorf("last failed login of the top IP = %v", o.TopIPs[0].Last)
	}
	if want := (secevents.Totals{FailedLogins: 6, Logins: 3, NewSourceLogins: 2, RootLogins: 2, SudoFailures: 2}); o.Totals != want {
		t.Errorf("totals = %+v, want %+v", o.Totals, want)
	}

	// One node; the top list bounded.
	q.Nodes, q.Top = []identity.NodeID{f.peer}, 1
	if o, err = f.store.Overview(ctx, scopeFor(t, f.org), q); err != nil || len(o.FailedLogins) != 1 || top(o.TopIPs) != "192.0.2.66=1/1/0" || o.Totals.Logins != 1 {
		t.Errorf("peer only: %+v, %v", o, err)
	}
	// A caller on the peer's group only.
	scope, err := tenant.ForGroups(org, nodeGroup(t, f.org, uuid.UUID(f.peer)))
	if err != nil {
		t.Fatal(err)
	}
	q.Nodes, q.Top = nil, 10
	if o, err = f.store.Overview(ctx, scope, q); err != nil || len(o.FailedLogins) != 1 || o.FailedLogins[0].Node != f.peer || o.Totals.FailedLogins != 1 {
		t.Errorf("group scope: %+v, %v", o, err)
	}
	// Another organization sees nothing.
	if o, err = f.store.Overview(ctx, scopeFor(t, orgB), q); err != nil || len(o.FailedLogins)+len(o.TopIPs)+len(o.TopUsers) != 0 || o.Totals != (secevents.Totals{}) {
		t.Errorf("org B: %+v, %v", o, err)
	}

	names, err := f.store.Hostnames(ctx, scope, []identity.NodeID{f.node, f.peer})
	if err != nil || len(names) != 1 || names[f.peer] == "" {
		t.Errorf("host names in the group scope = %v, %v", names, err)
	}
}

// The rule waits a week from when SerMon began to learn a node: its creation.
func TestFleetStore_NodesHaveTheStartOfLearning(t *testing.T) {
	f := newEventFixture(t)
	before := time.Now().Add(-time.Minute)
	nodes, err := postgres.NewFleetStore(open(t, setup(t).appDSN)).List(context.Background(), scopeFor(t, f.org))
	if err != nil || len(nodes) != 2 {
		t.Fatalf("List() = %v, %v", nodes, err)
	}
	for _, n := range nodes {
		if n.LearningSince.Before(before) || n.LearningSince.After(time.Now().Add(time.Minute)) {
			t.Errorf("%s learns since %v", n.Hostname, n.LearningSince)
		}
	}
}
