package notify_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// gated is a channel type whose sends wait at a gate and are counted while they run: a slow or
// dead target (M2-38).
type gated struct {
	*fakeNotifier
	kind notify.ChannelType
	gate chan struct{}

	mu             sync.Mutex
	active, most   int
	started, ended int
}

func newGated(kind notify.ChannelType) *gated {
	return &gated{fakeNotifier: &fakeNotifier{}, kind: kind, gate: make(chan struct{})}
}

func (g *gated) Type() notify.ChannelType { return g.kind }

func (g *gated) Send(ctx context.Context, c notify.Config, m notify.Message) error {
	g.mu.Lock()
	g.active++
	g.started++
	g.most = max(g.most, g.active)
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		g.active--
		g.ended++
		g.mu.Unlock()
	}()
	select {
	case <-g.gate:
	case <-ctx.Done():
		return ctx.Err()
	}
	return g.fakeNotifier.Send(ctx, c, m)
}

func (g *gated) count() (started, ended, most int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.started, g.ended, g.most
}

// fairFixture has a fast channel type and a gated one.
func fairFixture(t *testing.T, slow notify.Notifier, extra func(*notify.Deps)) *fixture {
	t.Helper()
	f := &fixture{store: newMemStore(), notifier: &fakeNotifier{}, clock: &clock{now: t0}, logs: &logs{}}
	registry, err := notify.NewRegistry(f.notifier, slow)
	if err != nil {
		t.Fatal(err)
	}
	f.deps = notify.Deps{
		Store: f.store, Registry: registry, Box: box(t), Policy: authz.NewPolicy(), BaseURL: base,
		Now: f.clock.Now, NewID: uuid.New, Logger: f.logs.logger(),
	}
	if extra != nil {
		extra(&f.deps)
	}
	f.svc = notify.NewService(f.deps)
	return f
}

func (f *fixture) channel(t *testing.T, org tenant.OrgID, kind notify.ChannelType) notify.Channel {
	t.Helper()
	nc := newChannel()
	nc.Type = kind
	c, err := f.svc.Create(context.Background(), admin(org, t0), nc)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (f *fixture) queue(t *testing.T, d *notify.Dispatcher, org tenant.OrgID, c notify.Channel, n int) {
	t.Helper()
	for range n {
		if err := d.Enqueue(context.Background(), org, []notify.ChannelID{c.ID}, bruteForce()); err != nil {
			t.Fatal(err)
		}
	}
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// One organization's dead target must not delay another's notifications (M2-28 finding, M2-38):
// organizations are delivered side by side.
func TestDispatcher_ASlowOrganizationDoesNotHoldUpAnother(t *testing.T) {
	slow := newGated("slow")
	f := fairFixture(t, slow, nil)
	d := f.dispatcher()
	f.queue(t, d, orgA, f.channel(t, orgA, "slow"), 1)
	f.queue(t, d, orgB, f.channel(t, orgB, "fake"), 1)
	done := make(chan struct{})
	go func() { d.Round(context.Background()); close(done) }()
	eventually(t, "organization B's notification", func() bool { return len(f.notifier.messages()) == 1 })
	select {
	case <-done:
		t.Fatal("the round ended although organization A's send still waits")
	default:
	}
	close(slow.gate)
	<-done
}

// Within an organization a few sends run at once, not all of them (M2-38).
func TestDispatcher_SendsInParallelUpToALimit(t *testing.T) {
	slow := newGated("slow")
	f := fairFixture(t, slow, nil)
	d := f.dispatcher()
	f.queue(t, d, orgA, f.channel(t, orgA, "slow"), 12)
	done := make(chan struct{})
	go func() { d.Round(context.Background()); close(done) }()
	eventually(t, "parallel sends", func() bool { started, _, _ := slow.count(); return started == notify.SendWorkers })
	time.Sleep(20 * time.Millisecond)
	if started, _, _ := slow.count(); started != notify.SendWorkers {
		t.Errorf("%d sends started, want %d at once", started, notify.SendWorkers)
	}
	close(slow.gate)
	<-done
	if started, ended, most := slow.count(); started != 12 || ended != 12 || most != notify.SendWorkers {
		t.Errorf("started %d, ended %d, at most %d at once", started, ended, most)
	}
}

// A delivery whose claim another instance took over (the claim expired meanwhile) is not written
// back: the newer attempt decides (fencing, M2-38).
func TestDispatcher_FinishesOnlyItsOwnClaim(t *testing.T) {
	f := newFixture(t)
	c := f.create(t)
	f.enqueue(t, f.dispatcher(), c.ID)
	claimed, err := f.store.Claim(context.Background(), scope(orgA), t0, t0.Add(notify.ClaimDuration), 1)
	if err != nil || len(claimed) != 1 {
		t.Fatal(claimed, err)
	}
	// Another instance claims it after the first claim ran out.
	if again, _ := f.store.Claim(context.Background(), scope(orgA), t0.Add(notify.ClaimDuration+time.Second), t0.Add(2*notify.ClaimDuration), 1); len(again) != 1 {
		t.Fatal("no takeover")
	}
	first := claimed[0]
	first.Status = notify.StatusSent
	if err := f.store.Finish(context.Background(), scope(orgA), first, claimed[0].ClaimedUntil); !errors.Is(err, notify.ErrClaimLost) {
		t.Errorf("finish with a stale claim: %v", err)
	}
	if got := f.store.all(orgA)[0]; got.Status != notify.StatusSending {
		t.Errorf("stale finish written: %+v", got)
	}
}

// Mail goes through the operator's server: an organization sends a bounded number per hour; the
// rest waits (M2-38).
func TestDispatcher_LimitsMailPerOrganization(t *testing.T) {
	mail := newGated("email")
	close(mail.gate)
	f := fairFixture(t, mail, func(d *notify.Deps) { d.MailsPerHour = 3 })
	d := f.dispatcher()
	f.queue(t, d, orgA, f.channel(t, orgA, "email"), 5)
	f.queue(t, d, orgB, f.channel(t, orgB, "email"), 1)
	d.Round(context.Background())
	if started, _, _ := mail.count(); started != 4 {
		t.Fatalf("%d mails sent, want 3 of A and 1 of B", started)
	}
	waiting := 0
	for _, del := range f.store.all(orgA) {
		if del.Status == notify.StatusPending && del.NextAttemptAt.After(t0) {
			waiting++
		}
	}
	if waiting != 2 {
		t.Errorf("%d deliveries of A wait for the next hour, want 2: %+v", waiting, f.store.all(orgA))
	}
	f.clock.advance(time.Hour + time.Minute)
	d.Round(context.Background())
	if started, _, _ := mail.count(); started != 6 {
		t.Errorf("%d mails after an hour, want all 6", started)
	}
}
