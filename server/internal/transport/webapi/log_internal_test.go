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
)

// M3-28: log queries hold Loki's answers in memory; a session, user, organization and the
// instance run only a few at once.
func TestLogQueries_AreBoundedPerSessionAndUser(t *testing.T) {
	h := NewLogHandler(nil, nil, slog.New(slog.DiscardHandler))
	org := tenant.OrgID(uuid.New())
	caller := func(user identity.UserID, sessionID string) (context.Context, access.Caller) {
		ctx := context.WithValue(context.Background(), sessionKey{}, session.Session{ID: sessionID, User: user})
		return ctx, access.Caller{Principal: authz.Principal{User: user, Org: org}}
	}
	alice, bob := identity.UserID(uuid.New()), identity.UserID(uuid.New())
	ctx, c := caller(alice, "s1")
	var releases []func()
	for range logQueryLimits.Session {
		release, err := h.admit(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	if _, err := h.admit(ctx, c); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Errorf("one more in the session: %v", err)
	}
	// Another session of Alice: up to her user limit.
	ctx2, c2 := caller(alice, "s2")
	for range logQueryLimits.User - logQueryLimits.Session {
		release, err := h.admit(ctx2, c2)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	if _, err := h.admit(ctx2, c2); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Errorf("one more for the user: %v", err)
	}
	// Bob is not affected; a finished query frees its slot.
	bctx, bc := caller(bob, "s3")
	if release, err := h.admit(bctx, bc); err != nil {
		t.Errorf("another user: %v", err)
	} else {
		release()
	}
	releases[0]()
	if _, err := h.admit(ctx, c); err != nil {
		t.Errorf("after one ended: %v", err)
	}
}
