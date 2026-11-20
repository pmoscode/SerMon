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
	// sessionOnly skips the principal: for services about the user itself (TwoFactorService),
	// which must work while the organization waits for the second-factor setup.
	sessionOnly bool
}

// SessionOnly returns an Authenticator that only requires a full session; the principal carries
// just the user.
func (a *Authenticator) SessionOnly() *Authenticator {
	only := *a
	only.sessionOnly = true
	return &only
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
		cookie := conn.RequestHeader().Get("Cookie")
		ctx, err := a.authenticate(ctx, cookie)
		if err != nil {
			return err
		}
		// Long streams check their session again (logout, removed membership, changed rights).
		// The check is no user activity: it must not keep an unattended session alive (M1-40).
		reauth := reauthenticator(func() (authz.Principal, error) {
			checked, err := a.authenticateWith(ctx, cookie, a.sessions.Peek)
			if err != nil {
				return authz.Principal{}, err
			}
			p, _ := PrincipalFrom(checked)
			return p, nil
		})
		return next(context.WithValue(ctx, reauthKey{}, reauth), conn)
	}
}

func (a *Authenticator) authenticate(ctx context.Context, cookieHeader string) (context.Context, error) {
	return a.authenticateWith(ctx, cookieHeader, a.sessions.Validate)
}

// authenticateWith checks the session with validate (Validate renews the activity, Peek doesn't).
func (a *Authenticator) authenticateWith(ctx context.Context, cookieHeader string,
	validate func(context.Context, string) (session.Session, error),
) (context.Context, error) {
	token, ok := tokenFrom(cookieHeaderOnly(cookieHeader))
	if !ok {
		return ctx, connect.NewError(connect.CodeUnauthenticated, errUnauthenticated)
	}
	s, err := validate(ctx, token)
	switch {
	case errors.Is(err, session.ErrInvalid):
		return ctx, connect.NewError(connect.CodeUnauthenticated, errUnauthenticated)
	case err != nil:
		return ctx, internalError(ctx, a.logger, "validate session", err)
	}
	if a.sessionOnly {
		return context.WithValue(context.WithValue(ctx, principalKey{}, authz.Principal{User: s.User}), sessionKey{}, s), nil
	}
	p, err := a.principals.Load(ctx, s)
	switch {
	case errors.Is(err, access.ErrNoMembership):
		return ctx, connect.NewError(connect.CodeUnauthenticated, errUnauthenticated)
	case errors.Is(err, access.ErrSecondFactorSetupRequired):
		return ctx, secondFactorSetupRequired()
	case err != nil:
		// A database outage must not look like a logout to the UI.
		return ctx, internalError(ctx, a.logger, "load principal", err)
	}
	return context.WithValue(context.WithValue(ctx, principalKey{}, p), sessionKey{}, s), nil
}
