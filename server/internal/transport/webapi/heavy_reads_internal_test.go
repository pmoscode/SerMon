package webapi

import (
	"context"
	"log/slog"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
)

// M4-31 (T106): the dashboards' reads share the database pool of every organization; at most half
// of it, a session, a user and an organization only part of that.
func TestNewHeavyReads_TakesAtMostHalfThePool(t *testing.T) {
	for pool, want := range map[int]streamLimits{
		4:  {Session: 2, User: 2, Org: 2, Total: 2},
		16: {Session: 2, User: 3, Org: 4, Total: 8},
		64: {Session: 2, User: 3, Org: 16, Total: 32},
		0:  {Session: 2, User: 2, Org: 2, Total: 2},
	} {
		if got := NewHeavyReads(pool).limiter.limits; got != want {
			t.Errorf("pool %d: %+v, want %+v", pool, got, want)
		}
	}
}

func TestHeavyReads_RefusesReadsBeyondTheLimits(t *testing.T) {
	reads := NewHeavyReads(16)
	org := tenant.OrgID(uuid.New())
	caller := func(user identity.UserID, sessionID string) (context.Context, access.Caller) {
		ctx := context.WithValue(context.Background(), sessionKey{}, session.Session{ID: sessionID, User: user})
		return ctx, access.Caller{Principal: authz.Principal{User: user, Org: org}}
	}
	alice := identity.UserID(uuid.New())
	ctx, c := caller(alice, "s1")
	var releases []func()
	for range 2 {
		readCtx, release, err := reads.admit(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := readCtx.Deadline(); !ok {
			t.Error("an admitted read has no deadline")
		}
		releases = append(releases, release)
	}
	if _, _, err := reads.admit(ctx, c); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Errorf("a third read of the session: %v", err)
	}
	// The handlers ask before they read: a full limit answers RESOURCE_EXHAUSTED.
	authed := context.WithValue(ctx, principalKey{}, c.Principal)
	full := map[string]error{}
	_, full["anomalies"] = (&AnomalyHandler{reads: reads, logger: slog.New(slog.DiscardHandler)}).ListUnusual(authed, connect.NewRequest(&webv1.ListUnusualRequest{}))
	_, full["capacity"] = (&CapacityHandler{reads: reads, logger: slog.New(slog.DiscardHandler)}).GetCapacity(authed, connect.NewRequest(&webv1.GetCapacityRequest{}))
	_, full["heatmap"] = (&HeatmapHandler{reads: reads, logger: slog.New(slog.DiscardHandler)}).GetHeatmap(authed, connect.NewRequest(&webv1.GetHeatmapRequest{}))
	for name, err := range full {
		if connect.CodeOf(err) != connect.CodeResourceExhausted {
			t.Errorf("%s with the session's reads taken: %v", name, err)
		}
	}
	releases[0]()
	if _, release, err := reads.admit(ctx, c); err != nil {
		t.Errorf("after a read ended: %v", err)
	} else {
		release()
	}
	if reads.limiter.open() != 1 {
		t.Errorf("open = %d, want 1", reads.limiter.open())
	}
}
