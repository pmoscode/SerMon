package webapi_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
	"github.com/pmoscode/SerMon/server/internal/transport/webapi"
)

var (
	testUser = identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001"))
	testOrg  = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
)

type memSessions struct{ m map[string]session.Session }

func (s *memSessions) Insert(_ context.Context, se session.Session) error {
	s.m[se.ID] = se
	return nil
}

func (s *memSessions) Get(_ context.Context, id string) (session.Session, bool, error) {
	se, ok := s.m[id]
	return se, ok, nil
}
func (s *memSessions) Touch(context.Context, string, time.Time) error               { return nil }
func (s *memSessions) SetStepUp(context.Context, string, time.Time) error           { return nil }
func (s *memSessions) Delete(_ context.Context, id string) error                    { delete(s.m, id); return nil }
func (s *memSessions) DeleteForUser(context.Context, identity.UserID, string) error { return nil }

type fakePrincipals struct{ err error }

func (f fakePrincipals) Load(_ context.Context, s session.Session) (authz.Principal, error) {
	if f.err != nil {
		return authz.Principal{}, f.err
	}
	return authz.Principal{User: s.User, Org: s.Org}, nil
}

func systemClient(t *testing.T, principals webapi.PrincipalLoader) (webv1connect.SystemServiceClient, string) {
	t.Helper()
	sessions := session.NewManager(&memSessions{m: map[string]session.Session{}}, time.Now)
	token, _, err := sessions.Create(context.Background(), testUser, testOrg)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	webapi.Mount(mux, webapi.Config{AllowedOrigins: []string{"https://ui.example"}}, webapi.Handlers{
		Auth:          webapi.NewAuthHandler(nil, sessions, nil, nil, slog.New(slog.DiscardHandler)),
		System:        webapi.NewSystemHandler("1.2.3"),
		Authenticator: webapi.NewAuthenticator(sessions, principals, slog.New(slog.DiscardHandler)),
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	hc := srv.Client()
	hc.Transport = originTransport{next: http.DefaultTransport, origin: "https://ui.example"}
	return webv1connect.NewSystemServiceClient(hc, srv.URL), token
}

func withCookie[T any](msg *T, token string) *connect.Request[T] {
	req := connect.NewRequest(msg)
	if token != "" {
		req.Header().Set("Cookie", webapi.SessionCookie+"="+token)
	}
	return req
}

func TestAuthenticator_RequiresAValidSession(t *testing.T) {
	c, token := systemClient(t, fakePrincipals{})
	ctx := context.Background()

	if _, err := c.GetVersion(ctx, withCookie(&webv1.GetVersionRequest{}, "")); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("without session: %v, want unauthenticated", err)
	}
	if _, err := c.GetVersion(ctx, withCookie(&webv1.GetVersionRequest{}, "forged-token")); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("forged session: %v, want unauthenticated", err)
	}
	res, err := c.GetVersion(ctx, withCookie(&webv1.GetVersionRequest{}, token))
	if err != nil || res.Msg.GetVersion() != "1.2.3" {
		t.Errorf("with session: %v, %v", res, err)
	}
}

func TestAuthenticator_RejectsSessionsOfRemovedMembers(t *testing.T) {
	c, token := systemClient(t, fakePrincipals{err: access.ErrNoMembership})

	if _, err := c.GetVersion(context.Background(), withCookie(&webv1.GetVersionRequest{}, token)); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("error = %v, want unauthenticated", err)
	}
}

func TestAuthenticator_InfrastructureErrorsAreInternalNotALogout(t *testing.T) {
	c, token := systemClient(t, fakePrincipals{err: errors.New("db down")})

	_, err := c.GetVersion(context.Background(), withCookie(&webv1.GetVersionRequest{}, token))
	if connect.CodeOf(err) != connect.CodeInternal || strings.Contains(err.Error(), "db down") {
		t.Errorf("error = %v, want a generic internal error", err)
	}
}
