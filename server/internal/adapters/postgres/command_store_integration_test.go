//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/domain/command"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
)

func newCommand(node identity.NodeID, kind command.Kind, at time.Time) command.Command {
	return command.Command{
		ID: uuid.Must(uuid.NewV7()), Node: node, Kind: kind, Scope: command.ScopeAll, Reason: "updates (SerMon)",
		CreatedAt: at, ExpiresAt: at.Add(command.DefaultExpiry),
	}
}

func commandIDs(cs []command.Command) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return out
}

// Two instances share the database: the one that stores a command wakes the other, which holds
// the node's session, through NOTIFY – with the node's ID only.
func TestCommandStore_ACommandWakesTheOtherInstance(t *testing.T) {
	f := newUpdateFixture(t, 0)
	a, b := open(t, setup(t).appDSN), open(t, setup(t).appDSN)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	woken := make(chan string, 4)
	go b.Listen(ctx, postgres.CommandsChannel, 50*time.Millisecond, func(p string) { woken <- p }, func(err error) { t.Log(err) })
	// LISTEN runs before the first command: wait for the connection.
	time.Sleep(300 * time.Millisecond)
	if err := postgres.NewCommandStore(a).Enqueue(ctx, f.org, newCommand(f.node, command.Update, updatesNow)); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-woken:
		if p != f.node.String() {
			t.Errorf("payload %q, want the node's ID", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the other instance was not woken")
	}
	// The other instance delivers it.
	got, err := postgres.NewCommandStore(b).Deliverable(ctx, f.org, f.node, false, updatesNow)
	if err != nil || len(got) != 1 || got[0].Kind != command.Update || got[0].Scope != command.ScopeAll {
		t.Errorf("deliverable on the other instance = %+v, %v", got, err)
	}
}

// Pending commands go out once per session; after a reconnect the sent ones without result go out
// again; expired ones never; a refresh is done once sent.
func TestCommandStore_DeliversOnceAndAgainAfterAReconnect(t *testing.T) {
	f := newUpdateFixture(t, 0)
	s := postgres.NewCommandStore(open(t, setup(t).appDSN))
	ctx := context.Background()
	update, refresh := newCommand(f.node, command.Update, updatesNow), newCommand(f.node, command.Refresh, updatesNow.Add(time.Second))
	old := newCommand(f.node, command.Reboot, updatesNow.Add(-time.Hour))
	other := newCommand(f.peer, command.Update, updatesNow)
	for _, c := range []command.Command{update, refresh, old, other} {
		if err := s.Enqueue(ctx, f.org, c); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.Deliverable(ctx, f.org, f.node, true, updatesNow.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if ids := commandIDs(first); len(ids) != 2 || ids[0] != update.ID || ids[1] != refresh.ID {
		t.Fatalf("deliverable = %+v, want the update and the refresh of this node, not the expired reboot", first)
	}
	if err := s.MarkSent(ctx, f.org, f.node, commandIDs(first), updatesNow.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.Deliverable(ctx, f.org, f.node, false, updatesNow.Add(4*time.Second)); len(again) != 0 {
		t.Errorf("sent again in the same session: %+v", again)
	}
	resend, _ := s.Deliverable(ctx, f.org, f.node, true, updatesNow.Add(5*time.Second))
	if ids := commandIDs(resend); len(ids) != 1 || ids[0] != update.ID {
		t.Errorf("after a reconnect = %+v, want the update only (the refresh is done)", resend)
	}
	for id, want := range map[uuid.UUID]command.State{update.ID: command.Sent, refresh.ID: command.Done, old.ID: command.Expired} {
		c, ok, err := s.Get(ctx, f.org, id)
		if err != nil || !ok || c.State != want {
			t.Errorf("state of %v = %+v, %v; want %d", id, c, err, want)
		}
	}
	// Past its expiry it goes no more; sent, it waits SentGrace for the agent's answer (M4-31).
	if late, _ := s.Deliverable(ctx, f.org, f.node, true, update.ExpiresAt); len(late) != 0 {
		t.Errorf("after the expiry = %+v", late)
	}
	if c, _, _ := s.Get(ctx, f.org, update.ID); c.State != command.Sent {
		t.Errorf("sent, at the expiry: %d", c.State)
	}
	if _, err := s.Deliverable(ctx, f.org, f.node, false, update.ExpiresAt.Add(command.SentGrace)); err != nil {
		t.Fatal(err)
	}
	if c, _, _ := s.Get(ctx, f.org, update.ID); c.State != command.Expired {
		t.Errorf("sent, after the grace: %d", c.State)
	}
}

// The agent's phases: a start, then one final phase; a resent or later phase changes nothing, nor
// does a result for another node's run or of another organization.
func TestCommandStore_RecordsThePhasesOfARunOnce(t *testing.T) {
	f := newUpdateFixture(t, 0)
	s := postgres.NewCommandStore(open(t, setup(t).appDSN))
	ctx := context.Background()
	c := newCommand(f.node, command.Update, updatesNow)
	if err := s.Enqueue(ctx, f.org, c); err != nil {
		t.Fatal(err)
	}
	started := updatesNow.Add(time.Minute)
	ok, err := s.Record(ctx, f.org, f.node, c.ID, command.Result{Phase: command.Started, StartedAt: &started})
	if err != nil || !ok {
		t.Fatalf("start = %v, %v", ok, err)
	}
	if got, _, _ := s.Get(ctx, f.org, c.ID); got.State != command.Running || got.Result.Phase != command.Started {
		t.Errorf("after the start = %+v", got)
	}
	if ok, _ := s.Record(ctx, f.org, f.node, c.ID, command.Result{Phase: command.Started}); ok {
		t.Error("a resent start was taken")
	}
	if ok, _ := s.Record(ctx, f.org, f.peer, c.ID, command.Result{Phase: command.Succeeded}); ok {
		t.Error("another node's result was taken")
	}
	other := newUpdateFixture(t, 0)
	if ok, _ := s.Record(ctx, other.org, f.node, c.ID, command.Result{Phase: command.Succeeded}); ok {
		t.Error("a result of another organization was taken")
	}
	ended := started.Add(time.Minute)
	out := "Setting up openssl\x00\x1b[31m" + strings.Repeat("x", command.MaxOutput)
	ok, err = s.Record(ctx, f.org, f.node, c.ID, command.Result{
		Phase: command.Failed, ExitCode: 100, Reason: "apt\u202efailed", Output: out, OutputTruncated: true, EndedAt: &ended,
	})
	if err != nil || !ok {
		t.Fatalf("failure = %v, %v", ok, err)
	}
	got, _, _ := s.Get(ctx, f.org, c.ID)
	r := got.Result
	if got.State != command.Done || r.Phase != command.Failed || r.ExitCode != 100 || !r.OutputTruncated || r.StartedAt == nil ||
		!r.StartedAt.Equal(started) || r.EndedAt == nil || r.Reason != "apt\uFFFDfailed" {
		t.Errorf("after the failure = %+v %+v", got, r)
	}
	// T41 (review M4-30): the end in the audit log, the start not.
	var entries int
	var details string
	queryOwner(t, setup(t).db.OwnerDSN, "SELECT count(*), coalesce(max(details::text), '') FROM audit_log WHERE org_id = $1 AND action = 'node.run_ended' AND target = $2",
		[]any{uuid.UUID(f.org), "node:" + uuid.UUID(f.node).String()}, &entries, &details)
	for _, want := range []string{`"phase": "failed"`, `"kind": "update"`, `"exit_code": "100"`, `"run": "` + c.ID.String()} {
		if entries != 1 || !strings.Contains(details, want) {
			t.Errorf("%d audit entries, %s lacks %s", entries, details, want)
		}
	}
	if len(r.Output) > command.MaxOutput || strings.Contains(r.Output, "\x00") || !strings.HasSuffix(r.Output, "xxx") {
		t.Errorf("output of %d bytes, NUL kept: %v", len(r.Output), strings.Contains(r.Output, "\x00"))
	}
	for _, p := range []command.Phase{command.Started, command.Succeeded} {
		if ok, _ := s.Record(ctx, f.org, f.node, c.ID, command.Result{Phase: p}); ok {
			t.Errorf("phase %d after the final one was taken", p)
		}
	}
	if ok, _ := s.Record(ctx, f.org, f.node, uuid.New(), command.Result{Phase: command.Succeeded}); ok {
		t.Error("an unknown run was taken")
	}
}

// A refresh on request: once per node within 5 minutes, 60 per organization a minute.
func TestCommandStore_RefreshesOnRequestAreLimited(t *testing.T) {
	f := newUpdateFixture(t, 0)
	s := postgres.NewCommandStore(open(t, setup(t).appDSN))
	ctx := context.Background()
	if err := s.RequestRefresh(ctx, f.org, f.node, identity.UserID{}, updatesNow); err != nil {
		t.Fatal(err)
	}
	if err := s.RequestRefresh(ctx, f.org, f.node, identity.UserID{}, updatesNow.Add(4*time.Minute)); !errors.Is(err, postgres.ErrTooSoon) {
		t.Errorf("a second refresh within 5 minutes = %v", err)
	}
	if err := s.RequestRefresh(ctx, f.org, f.peer, identity.UserID{}, updatesNow.Add(time.Second)); err != nil {
		t.Errorf("another node = %v", err)
	}
	if err := s.RequestRefresh(ctx, f.org, f.node, identity.UserID{}, updatesNow.Add(5*time.Minute+time.Second)); err != nil {
		t.Errorf("after 5 minutes = %v", err)
	}
	// The organization's limit: 60 a minute, counted over its nodes.
	nodes := make([]identity.NodeID, 0, postgres.OrgRefreshes)
	for range postgres.OrgRefreshes {
		n := uuid.New()
		createNode(t, setup(t).db.OwnerDSN, uuid.UUID(f.org), n)
		nodes = append(nodes, identity.NodeID(n))
	}
	at := updatesNow.Add(time.Hour)
	var refused int
	for _, n := range nodes {
		if err := s.RequestRefresh(ctx, f.org, n, identity.UserID{}, at); errors.Is(err, postgres.ErrTooSoon) {
			refused++
		}
	}
	if err := s.RequestRefresh(ctx, f.org, f.node, identity.UserID{}, at); !errors.Is(err, postgres.ErrTooSoon) || refused != 0 {
		t.Errorf("the 61st refresh of the minute = %v (%d refused before)", err, refused)
	}
	// Another organization's node can't be named.
	other := newUpdateFixture(t, 0)
	if err := postgres.NewCommandStore(open(t, setup(t).appDSN)).Enqueue(ctx, other.org, newCommand(f.node, command.Update, at)); err == nil {
		t.Error("a command for a node of another organization was stored")
	}
}

// M4-19: a failed self-update is a server event of the node; a successful one and other runs aren't.
func TestCommandStore_AFailedSelfUpdateIsAnEvent(t *testing.T) {
	f := newUpdateFixture(t, 0)
	s := postgres.NewCommandStore(open(t, setup(t).appDSN))
	ctx := context.Background()
	failed, ok, update := newCommand(f.node, command.SelfUpdate, updatesNow), newCommand(f.node, command.SelfUpdate, updatesNow), newCommand(f.node, command.Update, updatesNow)
	failed.Version, ok.Version = "1.1.0", "1.2.0"
	for _, c := range []command.Command{failed, ok, update} {
		if err := s.Enqueue(ctx, f.org, c); err != nil {
			t.Fatal(err)
		}
	}
	for c, phase := range map[uuid.UUID]command.Phase{failed.ID: command.Failed, ok.ID: command.Succeeded, update.ID: command.Failed} {
		if stored, err := s.Record(ctx, f.org, f.node, c, command.Result{Phase: phase, ExitCode: 1, Reason: map[bool]string{true: "rolled back"}[c == failed.ID]}); err != nil || !stored {
			t.Fatalf("record = %v, %v", stored, err)
		}
	}
	var events string
	queryOwner(t, setup(t).db.OwnerDSN, "SELECT coalesce(string_agg(kind || '=' || (details->>'reason'), ','), '') FROM security_events WHERE org_id = $1", []any{uuid.UUID(f.org)}, &events)
	if events != "agent.self_update_failed=rolled back" {
		t.Errorf("events = %q", events)
	}
	// A resend changes nothing and writes no second event.
	if stored, _ := s.Record(ctx, f.org, f.node, failed.ID, command.Result{Phase: command.Failed}); stored {
		t.Error("a resend was recorded")
	}
	var n int
	queryOwner(t, setup(t).db.OwnerDSN, "SELECT count(*) FROM security_events WHERE org_id = $1", []any{uuid.UUID(f.org)}, &n)
	if n != 1 {
		t.Errorf("%d events", n)
	}
}
