package webapi

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/logs"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/logql"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
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

// M3-29: a query too broad for the store is the user's to narrow (FAILED_PRECONDITION), not an
// invalid filter or an internal error.
func TestLogErrors_TooBroadIsAPrecondition(t *testing.T) {
	h := NewLogHandler(nil, nil, slog.New(slog.DiscardHandler))
	err := h.fail(context.Background(), "query logs", fmt.Errorf("query: %w", logs.ErrTooBroad))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("fail(ErrTooBroad) = %v", err)
	}
}

// M5-21: the sources of the API and of Loki map onto each other, the containers' included.
func TestLogSources_MapBothWays(t *testing.T) {
	for api, label := range map[webv1.LogSource]logql.Source{
		webv1.LogSource_LOG_SOURCE_JOURNAL:   logql.SourceJournal,
		webv1.LogSource_LOG_SOURCE_FILE:      logql.SourceFile,
		webv1.LogSource_LOG_SOURCE_CONTAINER: logql.SourceContainer,
	} {
		if got := sourceFrom(api); got != label {
			t.Errorf("sourceFrom(%v) = %v", api, got)
		}
		if got := logLineMessage(logs.Line{Source: label}).GetSource(); got != api {
			t.Errorf("logLineMessage(%v) = %v", label, got)
		}
	}
}
