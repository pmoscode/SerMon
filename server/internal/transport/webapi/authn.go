package webapi

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
)

// errUnauthenticated is the only error clients see for missing, expired or revoked sessions and
// for sessions whose membership was removed (not distinguished on purpose).
var errUnauthenticated = errors.New("not signed in")

// PrincipalLoader builds the authorization principal of a session (access.Loader).
type PrincipalLoader interface {
	Load(ctx context.Context, s session.Session) (authz.Principal, error)
}

// Authenticator is a Connect interceptor that requires a valid session cookie and puts the
// caller's principal into the context. Every service except AuthService is mounted with it.
type Authenticator struct {
	sessions   *session.Manager
	principals PrincipalLoader
	logger     *slog.Logger
}

// NewAuthenticator returns an Authenticator.
func NewAuthenticator(sessions *session.Manager, principals PrincipalLoader, logger *slog.Logger) *Authenticator {
	return &Authenticator{sessions: sessions, principals: principals, logger: logger}
}

type (
	principalKey struct{}
	sessionKey   struct{}
)

func sessionFrom(ctx context.Context) (session.Session, bool) {
	s, ok := ctx.Value(sessionKey{}).(session.Session)
	return s, ok
}

// PrincipalFrom returns the principal set by the Authenticator.
func PrincipalFrom(ctx context.Context) (authz.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(authz.Principal)
	return p, ok
}

// WrapUnary implements connect.Interceptor.
func (a *Authenticator) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		ctx, err := a.authenticate(ctx, req.Header().Get("Cookie"))
		if err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

// WrapStreamingClient implements connect.Interceptor (unused on the server).
func (a *Authenticator) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler implements connect.Interceptor.
func (a *Authenticator) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		ctx, err := a.authenticate(ctx, conn.RequestHeader().Get("Cookie"))
		if err != nil {
			return err
		}
		return next(ctx, conn)
	}
}

func (a *Authenticator) authenticate(ctx context.Context, cookieHeader string) (context.Context, error) {
	token, ok := tokenFrom(cookieHeaderOnly(cookieHeader))
	if !ok {
		return ctx, connect.NewError(connect.CodeUnauthenticated, errUnauthenticated)
	}
	s, err := a.sessions.Validate(ctx, token)
	switch {
	case errors.Is(err, session.ErrInvalid):
		return ctx, connect.NewError(connect.CodeUnauthenticated, errUnauthenticated)
	case err != nil:
		return ctx, internalError(ctx, a.logger, "validate session", err)
	}
	p, err := a.principals.Load(ctx, s)
	switch {
	case errors.Is(err, access.ErrNoMembership):
		return ctx, connect.NewError(connect.CodeUnauthenticated, errUnauthenticated)
	case err != nil:
		// A database outage must not look like a logout to the UI.
		return ctx, internalError(ctx, a.logger, "load principal", err)
	}
	return context.WithValue(context.WithValue(ctx, principalKey{}, p), sessionKey{}, s), nil
}
