package notify_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	"github.com/pmoscode/SerMon/server/internal/platform/safehttp"
)

func scope(org tenant.OrgID) tenant.Scope {
	s, err := tenant.ForAllGroups(org)
	if err != nil {
		panic(err)
	}
	return s
}

func (f *fixture) dispatcher() *notify.Dispatcher { return notify.NewDispatcher(f.deps) }

func (f *fixture) enqueue(t *testing.T, d *notify.Dispatcher, channels ...notify.ChannelID) {
	t.Helper()
	if err := d.Enqueue(context.Background(), orgA, channels, bruteForce()); err != nil {
		t.Fatal(err)
	}
}

func TestDispatcher_DeliversInTheChannelsLanguage(t *testing.T) {
	f := newFixture(t)
	c := f.create(t)
	d := f.dispatcher()
	f.enqueue(t, d, c.ID)
	if got := f.store.all(orgA); len(got) != 1 || got[0].Status != notify.StatusPending || got[0].Channel != c.ID || !got[0].NextAttemptAt.Equal(t0) {
		t.Fatalf("queued %+v", got)
	}
	d.Round(context.Background())
	msgs := f.notifier.messages()
	if len(msgs) != 1 {
		t.Fatalf("sent %d messages", len(msgs))
	}
	m := msgs[0].message
	if m.Lang != notify.German || !strings.HasPrefix(m.Title, "WARNUNG: SSH-Brute-Force") || m.Link != base+"/nodes/"+web.String() ||
		msgs[0].config.Secrets["token"] != secretToken {
		t.Errorf("message %+v with %+v", m, msgs[0].config)
	}
	got := f.store.all(orgA)[0]
	if got.Status != notify.StatusSent || got.Attempts != 1 || !got.SentAt.Equal(t0) || got.LastError != "" {
		t.Errorf("delivery = %+v", got)
	}
	if n := d.Stats().Sent.Load(); n != 1 {
		t.Errorf("sent counter %d", n)
	}
	d.Round(context.Background())
	if len(f.notifier.messages()) != 1 {
		t.Error("sent twice")
	}
}

func TestDispatcher_RetriesWithBackoffAndGivesUp(t *testing.T) {
	f := newFixture(t)
	c := f.create(t)
	d := f.dispatcher()
	f.enqueue(t, d, c.ID)
	for range notify.MaxAttempts {
		f.notifier.errs = append(f.notifier.errs, errors.New("connection reset"))
	}
	d.Round(context.Background())
	got := f.store.all(orgA)[0]
	if got.Status != notify.StatusPending || got.Attempts != 1 || !got.NextAttemptAt.Equal(t0.Add(30*time.Second)) || got.LastError != "connection reset" {
		t.Fatalf("after one failure: %+v", got)
	}
	// Not before it is due.
	f.clock.advance(20 * time.Second)
	d.Round(context.Background())
	if len(f.notifier.messages()) != 1 {
		t.Fatal("retried too early")
	}
	for range 20 {
		f.clock.advance(10 * time.Minute)
		d.Round(context.Background())
	}
	got = f.store.all(orgA)[0]
	if got.Status != notify.StatusFailed || got.Attempts != notify.MaxAttempts || len(f.notifier.messages()) != notify.MaxAttempts {
		t.Errorf("after all attempts: %+v, %d sends", got, len(f.notifier.messages()))
	}
	if d.Stats().Failed.Load() != 1 || d.Stats().Retried.Load() != notify.MaxAttempts-1 {
		t.Errorf("counters failed %d, retried %d", d.Stats().Failed.Load(), d.Stats().Retried.Load())
	}
}

func TestDispatcher_PermanentErrorsFailAtOnce(t *testing.T) {
	for name, err := range map[string]error{
		"rejected": notify.Permanent(errors.New("HTTP 404")),
		"blocked":  fmt.Errorf("POST hooks.example: %w", safehttp.ErrBlocked),
	} {
		f := newFixture(t)
		c := f.create(t)
		d := f.dispatcher()
		f.enqueue(t, d, c.ID)
		f.notifier.errs = []error{err}
		d.Round(context.Background())
		if got := f.store.all(orgA)[0]; got.Status != notify.StatusFailed || got.Attempts != 1 {
			t.Errorf("%s: %+v", name, got)
		}
		if name == "blocked" && d.Stats().Blocked.Load() != 1 {
			t.Errorf("blocked counter %d", d.Stats().Blocked.Load())
		}
	}
}

func TestDispatcher_RateLimitWaitsAsAsked(t *testing.T) {
	f := newFixture(t)
	c := f.create(t)
	d := f.dispatcher()
	f.enqueue(t, d, c.ID)
	f.notifier.errs = []error{&notify.RateLimitedError{RetryAfter: 3 * time.Minute}}
	d.Round(context.Background())
	if got := f.store.all(orgA)[0]; got.Status != notify.StatusPending || !got.NextAttemptAt.Equal(t0.Add(3*time.Minute)) {
		t.Errorf("rate limited: %+v", got)
	}
}

func TestDispatcher_DeletedOrDisabledChannel(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	gone, off := f.create(t), f.create(t)
	d := f.dispatcher()
	f.enqueue(t, d, gone.ID, off.ID)
	if err := f.svc.Delete(ctx, admin(orgA, t0), gone.ID); err != nil {
		t.Fatal(err)
	}
	disabled := false
	if _, err := f.svc.Update(ctx, admin(orgA, t0), off.ID, notify.ChannelChange{Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	d.Round(ctx)
	for _, got := range f.store.all(orgA) {
		if got.Status != notify.StatusFailed || !strings.Contains(got.LastError, "deleted or disabled") {
			t.Errorf("delivery %+v", got)
		}
	}
	if len(f.notifier.messages()) != 0 {
		t.Error("sent to a gone channel")
	}
}

// Errors of a channel may quote what the service answered; the stored and logged text never
// contains a secret of the channel and stays short.
func TestDispatcher_ErrorTextsHideSecrets(t *testing.T) {
	f := newFixture(t)
	c := f.create(t)
	d := f.dispatcher()
	f.enqueue(t, d, c.ID)
	f.notifier.errs = []error{fmt.Errorf("HTTP 500: %s\n%s", secretToken, strings.Repeat("x", 2000))}
	d.Round(context.Background())
	got := f.store.all(orgA)[0]
	if strings.Contains(got.LastError, secretToken) || len(got.LastError) > notify.MaxErrorBytes || strings.ContainsAny(got.LastError, "\n\r") {
		t.Errorf("stored error %q", got.LastError)
	}
	if strings.Contains(f.logs.buf.String(), secretToken) {
		t.Error("the secret was logged")
	}
}

// Two server instances share the queue: a claimed delivery is not sent again while the claim
// lasts; after the claim expires (an instance died), another one takes over.
func TestDispatcher_ClaimsAreExclusive(t *testing.T) {
	f := newFixture(t)
	c := f.create(t)
	a, b := f.dispatcher(), f.dispatcher()
	f.enqueue(t, a, c.ID)
	claimed, err := f.store.Claim(context.Background(), scope(orgA), t0, t0.Add(notify.ClaimDuration), 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim %v, %v", claimed, err)
	}
	b.Round(context.Background())
	if len(f.notifier.messages()) != 0 {
		t.Fatal("sent a delivery another instance holds")
	}
	f.clock.advance(notify.ClaimDuration + time.Second)
	a.Round(context.Background())
	if len(f.notifier.messages()) != 1 || f.store.all(orgA)[0].Status != notify.StatusSent {
		t.Errorf("after the claim expired: %d sends, %+v", len(f.notifier.messages()), f.store.all(orgA))
	}
}

func TestDispatcher_PurgesTheOldLog(t *testing.T) {
	f := newFixture(t)
	c := f.create(t)
	d := f.dispatcher()
	f.enqueue(t, d, c.ID)
	d.Round(context.Background())
	f.clock.advance(notify.Retention + time.Hour)
	f.enqueue(t, d, c.ID)
	d.Round(context.Background())
	if got := f.store.all(orgA); len(got) != 1 || !got[0].CreatedAt.Equal(f.clock.Now()) {
		t.Errorf("log after the retention: %+v", got)
	}
}

func TestEnqueue_ChecksTheChannels(t *testing.T) {
	f := newFixture(t)
	d := f.dispatcher()
	c := f.create(t)
	if err := d.Enqueue(context.Background(), orgB, []notify.ChannelID{c.ID}, bruteForce()); !errors.Is(err, notify.ErrChannelGone) {
		t.Errorf("channel of another organization: %v", err)
	}
	if len(f.store.all(orgB)) != 0 {
		t.Error("queued for another organization")
	}
}
