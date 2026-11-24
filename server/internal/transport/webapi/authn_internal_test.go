package webapi

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

type countingSessions struct {
	mu      sync.Mutex
	byID    map[string]session.Session
	touches int
}

func (c *countingSessions) Insert(_ context.Context, s session.Session) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byID[s.ID] = s
	return nil
}

func (c *countingSessions) Get(_ context.Context, id string) (session.Session, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.byID[id]
	return s, ok, nil
}

func (c *countingSessions) Touch(_ context.Context, id string, at time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.touches++
	s := c.byID[id]
	s.LastSeenAt = at
	c.byID[id] = s
	return nil
}

func (c *countingSessions) SetStepUp(context.Context, string, time.Time) error { return nil }

func (c *countingSessions) Delete(_ context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.byID, id)
	return nil
}

func (c *countingSessions) DeleteForUser(context.Context, identity.UserID, string) error { return nil }

type staticPrincipals struct{}

func (staticPrincipals) Load(_ context.Context, s session.Session) (authz.Principal, error) {
	return authz.Principal{User: s.User, Org: s.Org}, nil
}

// streamConn is the part of a streaming connection the Authenticator reads: the cookie.
type streamConn struct {
	connect.StreamingHandlerConn
	header http.Header
}

func (s streamConn) RequestHeader() http.Header { return s.header }

// The re-check of a running live view is no user activity: an open dashboard on an unattended
// machine must not keep the session alive past the idle timeout (M1-40).
func TestAuthenticator_StreamRecheckDoesNotRenewTheSession(t *testing.T) {
	store := &countingSessions{byID: map[string]session.Session{}}
	var mu sync.Mutex
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	sessions := session.NewManager(store, clock)
	token, _, err := sessions.Create(context.Background(), identity.UserID(uuid.New()), tenant.OrgID(uuid.New()))
	if err != nil {
		t.Fatal(err)
	}
	auth := NewAuthenticator(sessions, staticPrincipals{}, nil)
	header := http.Header{"Cookie": []string{SessionCookie + "=" + token}}
	touchesAtStart := 0

	err = auth.WrapStreamingHandler(func(ctx context.Context, _ connect.StreamingHandlerConn) error {
		reauth, _ := ctx.Value(reauthKey{}).(reauthenticator)
		store.mu.Lock()
		touchesAtStart = store.touches
		store.mu.Unlock()
		for range 5 {
			mu.Lock()
			now = now.Add(session.IdleTimeout / 4)
			mu.Unlock()
			if _, err := reauth(); err != nil {
				return err
			}
		}
		return nil
	})(context.Background(), streamConn{header: header})

	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("stream after the idle timeout without activity: %v, want UNAUTHENTICATED", err)
	}
	if store.touches != touchesAtStart {
		t.Errorf("the re-checks renewed the session %d times, want never", store.touches-touchesAtStart)
	}
}

// M3-28: a stream the UI reopens by itself (Sermon-Background) is no activity either; the user's
// own opens are.
func TestAuthenticator_BackgroundReopensDoNotRenewTheSession(t *testing.T) {
	for name, background := range map[string]bool{"the user's opens": false, "reopens by the UI": true} {
		t.Run(name, func(t *testing.T) {
			store := &countingSessions{byID: map[string]session.Session{}}
			now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
			sessions := session.NewManager(store, func() time.Time { return now })
			token, _, err := sessions.Create(context.Background(), identity.UserID(uuid.New()), tenant.OrgID(uuid.New()))
			if err != nil {
				t.Fatal(err)
			}
			auth := NewAuthenticator(sessions, staticPrincipals{}, nil)
			header := http.Header{"Cookie": []string{SessionCookie + "=" + token}}
			if background {
				header.Set(BackgroundHeader, "1")
			}
			open := func() error {
				return auth.WrapStreamingHandler(func(context.Context, connect.StreamingHandlerConn) error { return nil })(
					context.Background(), streamConn{header: header})
			}
			var err2 error
			for range 4 { // a reopen every 15 minutes, an hour in all
				now = now.Add(15 * time.Minute)
				if err2 = open(); err2 != nil {
					break
				}
			}
			if background && connect.CodeOf(err2) != connect.CodeUnauthenticated {
				t.Errorf("reopens by the UI kept the session alive: %v", err2)
			}
			if !background && err2 != nil {
				t.Errorf("the user's own opens: %v", err2)
			}
		})
	}
}
