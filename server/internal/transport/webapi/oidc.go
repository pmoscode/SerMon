package webapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/auth/oidc"
	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
)

// OIDCCookie binds a started sign-in at the provider to the browser (M3-20). SameSite=Lax: the
// provider sends the browser back with a top-level GET, which carries it.
const OIDCCookie = "__Host-sermon_oidc"

// OIDCCallbackPath is where the provider returns to.
const OIDCCallbackPath = "/auth/oidc/callback"

// Where the browser lands after a failed or incomplete sign-in (the login page explains).
const (
	oidcFailed       = "/login?sso=failed"
	oidcTaken        = "/settings/security?sso=taken"
	oidcSecondFactor = "/login?sso=second_factor"
)

// WithOIDC turns on the sign-in at the provider (M3-20); name is the provider's on the button.
func (h *AuthHandler) WithOIDC(svc *oidc.Service, name string) *AuthHandler {
	h.oidc, h.oidcName = svc, name
	return h
}

// GetLoginOptions tells the login page whether there is a provider.
func (h *AuthHandler) GetLoginOptions(context.Context, *connect.Request[webv1.GetLoginOptionsRequest]) (*connect.Response[webv1.GetLoginOptionsResponse], error) {
	return connect.NewResponse(&webv1.GetLoginOptionsResponse{Oidc: h.oidc != nil, OidcName: h.oidcName}), nil
}

func oidcCookie(value string, maxAge time.Duration) *http.Cookie {
	c := &http.Cookie{
		Name: OIDCCookie, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
		MaxAge: int(maxAge.Seconds()),
	}
	if maxAge < 0 {
		c.MaxAge = -1
	}
	return c
}

// BeginOidc starts a sign-in at the provider.
func (h *AuthHandler) BeginOidc(ctx context.Context, req *connect.Request[webv1.BeginOidcRequest]) (*connect.Response[webv1.BeginOidcResponse], error) {
	if h.oidc == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("no OIDC provider is configured"))
	}
	token, _ := tokenFrom(req.Header())
	var intent oidc.Intent
	switch req.Msg.GetIntent() {
	case webv1.OidcIntent_OIDC_INTENT_LOGIN:
		intent = oidc.IntentLogin
	case webv1.OidcIntent_OIDC_INTENT_LINK:
		intent = oidc.IntentLink
	case webv1.OidcIntent_OIDC_INTENT_STEP_UP:
		intent = oidc.IntentStepUp
	case webv1.OidcIntent_OIDC_INTENT_UNSPECIFIED:
		fallthrough
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("unknown intent"))
	}
	begun, err := h.oidc.Begin(ctx, intent, token, req.Msg.GetNext(), ClientIPFrom(ctx))
	switch {
	case errors.Is(err, oidc.ErrTooManyAttempts):
		return nil, connect.NewError(connect.CodeResourceExhausted, err)
	case errors.Is(err, oidc.ErrNotSignedIn):
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	case errors.Is(err, access.ErrStepUpRequired):
		return nil, stepUpRequired()
	case err != nil:
		h.logger.WarnContext(ctx, "oidc: provider unavailable", "error", err.Error())
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("the identity provider can't be reached"))
	}
	res := connect.NewResponse(&webv1.BeginOidcResponse{AuthorizationUrl: begun.URL})
	res.Header().Add("Set-Cookie", oidcCookie(begun.Binding, oidc.FlowLifetime).String())
	return res, nil
}

// OIDCCallback is the endpoint the provider returns to (a plain GET, no Connect): it completes
// the flow, sets the session cookie of a sign-in and sends the browser on. Every failure lands
// on the same page; the reason is in the server log.
func (h *AuthHandler) OIDCCallback() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The code is in this URL: nothing may carry it on.
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		http.SetCookie(w, oidcCookie("", -1))
		if h.oidc == nil {
			http.Redirect(w, r, oidcFailed, http.StatusSeeOther)
			return
		}
		q := r.URL.Query()
		binding := ""
		if c, err := r.Cookie(OIDCCookie); err == nil {
			binding = c.Value
		}
		token, _ := tokenFrom(r.Header)
		code := q.Get("code")
		if q.Get("error") != "" {
			code = "" // the provider refused: the flow ends as failed
		}
		res, err := h.oidc.Complete(r.Context(), q.Get("state"), code, binding, token, ClientIPFrom(r.Context()))
		switch {
		case errors.Is(err, oidc.ErrIdentityTaken):
			http.Redirect(w, r, oidcTaken, http.StatusSeeOther)
			return
		case err != nil:
			if !errors.Is(err, oidc.ErrFailed) && !errors.Is(err, oidc.ErrUnknownIdentity) && !errors.Is(err, oidc.ErrTooManyAttempts) {
				h.logger.ErrorContext(r.Context(), "oidc callback failed", "error", err.Error())
			}
			http.Redirect(w, r, oidcFailed, http.StatusSeeOther)
			return
		}
		next := res.Next
		if res.Intent == oidc.IntentLogin {
			if res.Pending {
				http.SetCookie(w, sessionCookie(res.Token, session.PendingTimeout))
				next = oidcSecondFactor + "&redirect=" + url.QueryEscape(res.Next)
			} else {
				http.SetCookie(w, sessionCookie(res.Token, session.AbsoluteTimeout))
			}
		}
		http.Redirect(w, r, next, http.StatusSeeOther)
	})
}

// ListOidcIdentities lists the identities of the signed-in user (M3-22).
func (h *AuthHandler) ListOidcIdentities(ctx context.Context, req *connect.Request[webv1.ListOidcIdentitiesRequest]) (*connect.Response[webv1.ListOidcIdentitiesResponse], error) {
	res := &webv1.ListOidcIdentitiesResponse{}
	token, ok := tokenFrom(req.Header())
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, session.ErrInvalid)
	}
	if h.oidc == nil {
		if _, err := h.sessions.Validate(ctx, token); err != nil {
			return nil, connect.NewError(connect.CodeUnauthenticated, session.ErrInvalid)
		}
		return connect.NewResponse(res), nil
	}
	ids, err := h.oidc.Identities(ctx, token)
	if errors.Is(err, oidc.ErrNotSignedIn) {
		return nil, connect.NewError(connect.CodeUnauthenticated, session.ErrInvalid)
	}
	if err != nil {
		return nil, h.internal(ctx, "list oidc identities", err)
	}
	for _, i := range ids {
		v := &webv1.OidcIdentity{Issuer: i.Issuer, Subject: i.Subject, Email: i.Email, CreateTime: timestamppb.New(i.CreatedAt)}
		if !i.LastLoginAt.IsZero() {
			v.LastLoginTime = timestamppb.New(i.LastLoginAt)
		}
		res.Identities = append(res.Identities, v)
	}
	return connect.NewResponse(res), nil
}

// UnlinkOidcIdentity removes an identity of the signed-in user (M3-22).
func (h *AuthHandler) UnlinkOidcIdentity(ctx context.Context, req *connect.Request[webv1.UnlinkOidcIdentityRequest]) (*connect.Response[webv1.UnlinkOidcIdentityResponse], error) {
	token, _ := tokenFrom(req.Header())
	if h.oidc == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("no OIDC provider is configured"))
	}
	err := h.oidc.Unlink(ctx, token, req.Msg.GetIssuer())
	switch {
	case errors.Is(err, oidc.ErrNotSignedIn):
		return nil, connect.NewError(connect.CodeUnauthenticated, session.ErrInvalid)
	case errors.Is(err, access.ErrStepUpRequired):
		return nil, stepUpRequired()
	case errors.Is(err, oidc.ErrLastSignIn):
		return nil, connect.NewError(connect.CodeFailedPrecondition, oidc.ErrLastSignIn)
	case errors.Is(err, oidc.ErrNotLinked):
		return nil, connect.NewError(connect.CodeNotFound, oidc.ErrNotLinked)
	case err != nil:
		return nil, h.internal(ctx, "unlink oidc identity", err)
	}
	return connect.NewResponse(&webv1.UnlinkOidcIdentityResponse{}), nil
}
