package webapi

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/kiosk"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

func display(org tenant.OrgID, creator identity.UserID) kiosk.Display {
	return kiosk.Display{Token: kiosk.TokenID(uuid.New()), Org: org, Creator: creator}
}

// M5-33: a display reads a few widgets at once, the others wait for a slot; one that waits too
// long fails (RESOURCE_EXHAUSTED); other displays are not affected.
func TestKioskReads_BoundPerTokenAndWait(t *testing.T) {
	r := NewKioskReads(NewLogQueries())
	r.wait = 50 * time.Millisecond
	org := tenant.OrgID(uuid.New())
	d := display(org, identity.UserID(uuid.New()))
	ctx := context.Background()
	var releases []func()
	for range KioskReadsPerToken {
		release, err := r.Admit(ctx, d, false)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	if _, err := r.Admit(ctx, d, false); !errors.Is(err, kiosk.ErrBusy) {
		t.Errorf("one more of the display: %v, want ErrBusy after the wait", err)
	}
	if release, err := r.Admit(ctx, display(org, d.Creator), false); err != nil {
		t.Errorf("another display: %v", err)
	} else {
		release()
	}
	// A waiting read gets the slot a finished one frees.
	r.wait = 5 * time.Second
	got := make(chan error, 1)
	go func() {
		release, err := r.Admit(ctx, d, false)
		if err == nil {
			release()
		}
		got <- err
	}()
	time.Sleep(20 * time.Millisecond)
	releases[0]()
	if err := <-got; err != nil {
		t.Errorf("waiting read: %v", err)
	}
	for _, release := range releases[1:] {
		release()
	}
	if n := r.tracked(); n != 0 {
		t.Errorf("%d tokens still tracked after all reads ended", n)
	}
}

// Log widgets count against the log queries of the UI (M3-28): the creator's and the
// organization's limits hold for the displays too.
func TestKioskReads_LogsShareTheLimitsOfTheLogQueries(t *testing.T) {
	queries := NewLogQueries()
	r := NewKioskReads(queries)
	r.wait = 10 * time.Millisecond
	h := NewLogHandler(nil, nil, queries, slog.New(slog.DiscardHandler))
	org := tenant.OrgID(uuid.New())
	alice := identity.UserID(uuid.New())
	// Alice's own log queries in the UI take her user limit.
	c := access.Caller{Principal: authz.Principal{User: alice, Org: org}}
	var releases []func()
	for i := range logQueryLimits.User {
		ctx := context.WithValue(context.Background(), sessionKey{}, session.Session{ID: "s" + string(rune('a'+i/logQueryLimits.Session)), User: alice})
		release, err := h.admit(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	d := display(org, alice)
	if _, err := r.Admit(context.Background(), d, true); !errors.Is(err, kiosk.ErrBusy) {
		t.Errorf("log widget of alice's display: %v, want ErrBusy", err)
	}
	release, err := r.Admit(context.Background(), d, false)
	if err != nil {
		t.Fatalf("metric widget: %v", err)
	}
	release()
	releases[0]()
	release, err = r.Admit(context.Background(), d, true)
	if err != nil {
		t.Fatalf("after one ended: %v", err)
	}
	if open := queries.limiter.open(); open != logQueryLimits.User {
		t.Errorf("open log queries = %d, want %d", open, logQueryLimits.User)
	}
	release()
	if open := queries.limiter.open(); open != logQueryLimits.User-1 {
		t.Errorf("after the display's read: %d open, want %d", open, logQueryLimits.User-1)
	}
	for _, release := range releases[1:] {
		release()
	}
}
