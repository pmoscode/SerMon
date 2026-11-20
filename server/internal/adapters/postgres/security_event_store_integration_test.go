//go:build integration

package postgres_test

import (
	"context"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/app/secevents"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/secevent"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// eventsNow is inside the partitions of the migration.
var eventsNow = time.Now().UTC().Truncate(time.Second)

type eventFixture struct {
	store      *postgres.SecurityEventStore
	org        uuid.UUID
	node, peer identity.NodeID
}

// A fresh organization with two nodes in different groups.
func newEventFixture(t *testing.T) eventFixture {
	t.Helper()
	f := setup(t)
	org := uuid.New()
	ownerExec(t, f.db.OwnerDSN, "INSERT INTO organizations (id, name) VALUES ($1, 'Events')", org)
	node, peer := uuid.New(), uuid.New()
	createNode(t, f.db.OwnerDSN, org, node)
	createNode(t, f.db.OwnerDSN, org, peer)
	store := postgres.NewSecurityEventStore(open(t, f.appDSN))
	return eventFixture{store: store, org: org, node: identity.NodeID(node), peer: identity.NodeID(peer)}
}

func failedLogin(node identity.NodeID, at time.Time, user, ip string) secevent.Event {
	return secevent.Event{
		Node: node, OccurredAt: at, Kind: secevent.SSHLoginFailed, User: user,
		Remote: &secevent.Remote{IP: netip.MustParseAddr(ip), Port: 22}, Details: secevent.Details{Method: "password"},
	}
}

func (f eventFixture) list(t *testing.T, scope tenant.Scope, filter secevents.Filter) []secevent.Event {
	t.Helper()
	if filter.From.IsZero() {
		filter.From, filter.To = eventsNow.Add(-24*time.Hour), eventsNow.Add(time.Hour)
	}
	if filter.Limit == 0 {
		filter.Limit = 100
	}
	events, err := f.store.List(context.Background(), scope, filter)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func TestSecurityEventStore_WritesEachBatchOnce(t *testing.T) {
	f := newEventFixture(t)
	ctx := context.Background()
	org := tenant.OrgID(f.org)
	batch := func(seq uint64, users ...string) secevents.Batch {
		b := secevents.Batch{Seq: seq}
		for i, u := range users {
			b.Events = append(b.Events, failedLogin(f.node, eventsNow.Add(-time.Duration(seq*10+uint64(i))*time.Second), u, "192.0.2.7"))
		}
		return b
	}
	written, err := f.store.Write(ctx, org, f.node, []secevents.Batch{batch(1, "a", "b"), batch(2, "c")})
	if err != nil || len(written) != 2 {
		t.Fatalf("Write() = %d batches, %v", len(written), err)
	}
	// After a reconnect the agent sends 2 again, then 3.
	written, err = f.store.Write(ctx, org, f.node, []secevents.Batch{batch(2, "c"), batch(3, "d")})
	if err != nil || len(written) != 1 || written[0].Seq != 3 {
		t.Fatalf("resend: Write() = %+v, %v; want only batch 3", written, err)
	}
	got := f.list(t, scopeFor(t, f.org), secevents.Filter{})
	if len(got) != 4 {
		t.Fatalf("%d events, want a, b, c, d once", len(got))
	}
	e := got[3] // oldest
	if e.User != "d" || e.Remote == nil || e.Remote.IP != netip.MustParseAddr("192.0.2.7") || e.Remote.Port != 22 ||
		e.Details.Method != "password" || e.ID == 0 || e.ReceivedAt.IsZero() || e.Node != f.node {
		t.Errorf("stored event = %+v", e)
	}
}

// Two sessions of a node (two server instances during a takeover) write the same batch.
func TestSecurityEventStore_ConcurrentResendsAreStoredOnce(t *testing.T) {
	f := newEventFixture(t)
	batch := secevents.Batch{Seq: 5, Events: []secevent.Event{failedLogin(f.node, eventsNow, "root", "192.0.2.9")}}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if _, err := f.store.Write(context.Background(), tenant.OrgID(f.org), f.node, []secevents.Batch{batch}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if got := f.list(t, scopeFor(t, f.org), secevents.Filter{}); len(got) != 1 {
		t.Errorf("%d events, want 1", len(got))
	}
}

func TestSecurityEventStore_KeepsOrganizationsApart(t *testing.T) {
	f := newEventFixture(t)
	ctx := context.Background()
	if _, err := f.store.Write(ctx, tenant.OrgID(f.org), f.node, []secevents.Batch{{Seq: 1, Events: []secevent.Event{failedLogin(f.node, eventsNow, "root", "192.0.2.1")}}}); err != nil {
		t.Fatal(err)
	}
	if got := f.list(t, scopeFor(t, orgB), secevents.Filter{}); len(got) != 0 {
		t.Errorf("org B sees %d events of another organization", len(got))
	}
	// Org B writing for a node of the fixture's organization: the node is invisible to it.
	_, err := f.store.Write(ctx, tenant.OrgID(orgB), f.node, []secevents.Batch{{Seq: 9, Events: []secevent.Event{failedLogin(f.node, eventsNow, "x", "192.0.2.2")}}})
	if err == nil {
		t.Error("a write for a foreign node succeeded")
	}
	if got := f.list(t, scopeFor(t, f.org), secevents.Filter{}); len(got) != 1 {
		t.Errorf("%d events, want only the own one", len(got))
	}
}

func TestSecurityEventStore_Filters(t *testing.T) {
	f := newEventFixture(t)
	ctx := context.Background()
	reboot := secevent.Event{Node: f.peer, OccurredAt: eventsNow.Add(-time.Minute), Kind: secevent.SystemReboot, Details: secevent.Details{PreviousBootID: "cc8f"}}
	host := secevent.Event{Node: f.node, OccurredAt: eventsNow.Add(-2 * time.Minute), Kind: secevent.SSHInvalidUser, User: "admin", Remote: &secevent.Remote{Host: "client.example.org", Port: 2222}}
	events := []secevent.Event{
		failedLogin(f.node, eventsNow.Add(-3*time.Minute), "root", "192.0.2.7"),
		failedLogin(f.node, eventsNow.Add(-4*time.Minute), "alice", "198.51.100.1"),
		failedLogin(f.node, eventsNow.Add(-5*time.Minute), "root", "2001:db8::7"),
		failedLogin(f.node, eventsNow.Add(-48*time.Hour), "old", "192.0.2.8"),
		reboot, host,
	}
	if _, err := f.store.Write(ctx, tenant.OrgID(f.org), f.node, []secevents.Batch{{Seq: 1, Events: events}}); err != nil {
		t.Fatal(err)
	}
	all := scopeFor(t, f.org)
	users := func(list []secevent.Event) string {
		var out []string
		for _, e := range list {
			out = append(out, string(e.Kind)+":"+e.User)
		}
		return strings.Join(out, ",")
	}
	root := "root"
	v4 := netip.MustParsePrefix("192.0.2.0/24")
	v6 := netip.MustParsePrefix("2001:db8::/32")
	for name, tc := range map[string]struct {
		filter secevents.Filter
		want   string
	}{
		"newest first":   {secevents.Filter{}, "system.reboot:,ssh.login.invalid_user:admin,ssh.login.failed:root,ssh.login.failed:alice,ssh.login.failed:root"},
		"kind":           {secevents.Filter{Kinds: []secevent.Kind{secevent.SystemReboot, secevent.SSHInvalidUser}}, "system.reboot:,ssh.login.invalid_user:admin"},
		"user":           {secevents.Filter{User: &root}, "ssh.login.failed:root,ssh.login.failed:root"},
		"ipv4 network":   {secevents.Filter{Network: &v4}, "ssh.login.failed:root"},
		"ipv6 network":   {secevents.Filter{Network: &v6}, "ssh.login.failed:root"},
		"node":           {secevents.Filter{Nodes: []identity.NodeID{f.peer}}, "system.reboot:"},
		"time range":     {secevents.Filter{From: eventsNow.Add(-72 * time.Hour), To: eventsNow.Add(-24 * time.Hour)}, "ssh.login.failed:old"},
		"page of two":    {secevents.Filter{Limit: 2}, "system.reboot:,ssh.login.invalid_user:admin"},
		"after the page": {secevents.Filter{Limit: 2, Before: &secevents.Cursor{At: host.OccurredAt, ID: 0}}, "ssh.login.failed:root,ssh.login.failed:alice"},
	} {
		if got := users(f.list(t, all, tc.filter)); got != tc.want {
			t.Errorf("%s: %s\n want %s", name, got, tc.want)
		}
	}
	got := f.list(t, all, secevents.Filter{Kinds: []secevent.Kind{secevent.SSHInvalidUser}})
	if r := got[0].Remote; r == nil || r.Host != "client.example.org" || r.IP.IsValid() || r.Port != 2222 {
		t.Errorf("host remote = %+v", r)
	}
	got = f.list(t, all, secevents.Filter{Kinds: []secevent.Kind{secevent.SystemReboot}})
	if got[0].Remote != nil || got[0].Details.PreviousBootID != "cc8f" || got[0].User != "" {
		t.Errorf("reboot = %+v", got[0])
	}
}

// A caller on one group sees the events of its nodes only.
func TestSecurityEventStore_GroupScope(t *testing.T) {
	f := newEventFixture(t)
	ctx := context.Background()
	events := []secevent.Event{failedLogin(f.node, eventsNow, "node", "192.0.2.1"), failedLogin(f.peer, eventsNow, "peer", "192.0.2.2")}
	if _, err := f.store.Write(ctx, tenant.OrgID(f.org), f.node, []secevents.Batch{{Seq: 1, Events: events}}); err != nil {
		t.Fatal(err)
	}
	group := nodeGroup(t, f.org, uuid.UUID(f.peer))
	scope, err := tenant.ForGroups(tenant.OrgID(f.org), group)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.list(t, scope, secevents.Filter{}); len(got) != 1 || got[0].User != "peer" {
		t.Errorf("group scope sees %+v", got)
	}
}

func nodeGroup(t *testing.T, org, node uuid.UUID) tenant.GroupID {
	t.Helper()
	f := setup(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, f.db.OwnerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var group uuid.UUID
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT set_config('sermon.org_id', $1, true)", org.String()); err != nil {
			return err
		}
		return tx.QueryRow(ctx, "SELECT group_id FROM nodes WHERE id = $1", node).Scan(&group)
	})
	if err != nil {
		t.Fatal(err)
	}
	return tenant.GroupID(group)
}

// The application appends and reads through the parent table only; partitions are made by the
// definer functions.
func TestSecurityEventStore_IsAppendOnlyAndPartitionsAreNotTouchable(t *testing.T) {
	f := newEventFixture(t)
	ctx := context.Background()
	if _, err := f.store.Write(ctx, tenant.OrgID(f.org), f.node, []secevents.Batch{{Seq: 1, Events: []secevent.Event{failedLogin(f.node, eventsNow, "root", "192.0.2.1")}}}); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, setup(t).appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	partition := "security_events_" + eventsNow.Format("200601")
	for _, stmt := range []string{
		"UPDATE security_events SET user_name = 'x'",
		"DELETE FROM security_events",
		"SELECT count(*) FROM " + partition,
		"INSERT INTO " + partition + " (org_id, node_id, occurred_at, kind) VALUES ('" + f.org.String() + "', '" + uuid.UUID(f.node).String() + "', now(), 'system.reboot')",
		"CREATE TABLE security_events_209901 PARTITION OF security_events FOR VALUES FROM ('2099-01-01') TO ('2099-02-01')",
		"DROP TABLE " + partition,
	} {
		err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, "SELECT set_config('sermon.org_id', $1, true)", f.org.String()); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, stmt)
			return err
		})
		if err == nil {
			t.Errorf("sermon_app may run %q", stmt)
		}
	}
}

// One year of retention (ADR 0018): whole months are dropped. The first month of the migration
// (2026-10) stands in for an expired one.
func TestSecurityEventStore_RetentionDropsOldPartitions(t *testing.T) {
	first := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if eventsNow.Before(first.AddDate(0, 1, 0)) {
		t.Skip("needs a current month after the first partition")
	}
	f := newEventFixture(t)
	ctx := context.Background()
	// Put the month back as the migration made it, for the next test run in this process.
	t.Cleanup(func() {
		ownerExec(t, setup(t).db.OwnerDSN, `DO $$
		BEGIN
			IF to_regclass('public.security_events_202610') IS NULL THEN
				GRANT CREATE ON SCHEMA public TO sermon_definer;
				CREATE TABLE security_events_202610 PARTITION OF security_events
					FOR VALUES FROM ('2026-10-01 00:00+00') TO ('2026-11-01 00:00+00');
				ALTER TABLE security_events_202610 ENABLE ROW LEVEL SECURITY;
				ALTER TABLE security_events_202610 FORCE ROW LEVEL SECURITY;
				ALTER TABLE security_events_202610 OWNER TO sermon_definer;
				REVOKE CREATE ON SCHEMA public FROM sermon_definer;
			END IF;
		END $$`)
	})
	old := first.Add(14 * 24 * time.Hour)
	events := []secevent.Event{failedLogin(f.node, old, "old", "192.0.2.1"), failedLogin(f.node, eventsNow, "new", "192.0.2.2")}
	if _, err := f.store.Write(ctx, tenant.OrgID(f.org), f.node, []secevents.Batch{{Seq: 1, Events: events}}); err != nil {
		t.Fatal(err)
	}
	if dropped, err := f.store.Expire(ctx, first.AddDate(0, 1, 0).Add(-time.Second)); err != nil || dropped != 0 {
		t.Fatalf("Expire() before the end of the month = %d, %v; whole months only", dropped, err)
	}
	dropped, err := f.store.Expire(ctx, first.AddDate(0, 1, 0))
	if err != nil || dropped != 1 {
		t.Fatalf("Expire() = %d, %v; want the first month", dropped, err)
	}
	got := f.list(t, scopeFor(t, f.org), secevents.Filter{From: first, To: eventsNow.Add(time.Hour)})
	if len(got) != 1 || got[0].User != "new" {
		t.Errorf("after the retention: %+v, want only the new event", got)
	}
	// Writing into the dropped month fails instead of landing somewhere else; ingest keeps
	// event times within a week (secevents, ingest.Config.EventMaxAge).
	if _, err := f.store.Write(ctx, tenant.OrgID(f.org), f.node, []secevents.Batch{{Seq: 2, Events: events[:1]}}); err == nil {
		t.Error("an event of a dropped month was stored")
	}
}

// The migration creates the partitions ten years ahead; the maintenance warns a year before the
// end (secevents.HorizonWarning).
func TestSecurityEventStore_HorizonIsYearsAhead(t *testing.T) {
	f := newEventFixture(t)
	horizon, err := f.store.Horizon(context.Background())
	if err != nil || !horizon.Equal(time.Date(2037, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("Horizon() = %v, %v; want 2037-01-01", horizon, err)
	}
}
