package webapi

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/auth/passkey"
	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// PasskeyCookie binds a sign-in with a passkey to the browser that began it (M3-23).
const PasskeyCookie = "__Host-sermon_webauthn" //nolint:gosec // G101: a cookie name, not a credential

// PasskeyHandler implements sermon.web.v1.PasskeyService (behind the session-only
// Authenticator, like TwoFactorService).
type PasskeyHandler struct {
	svc    *passkey.Service
	logger *slog.Logger
}

var _ webv1connect.PasskeyServiceHandler = (*PasskeyHandler)(nil)

// NewPasskeyHandler returns the handler.
func NewPasskeyHandler(svc *passkey.Service, logger *slog.Logger) *PasskeyHandler {
	return &PasskeyHandler{svc: svc, logger: logger}
}

func passkeyView(p passkey.Passkey) *webv1.Passkey {
	v := &webv1.Passkey{
		Id: base64.RawURLEncoding.EncodeToString(p.ID), Name: p.Name, CreateTime: timestamppb.New(p.CreatedAt),
		Synced: p.Credential.Flags.BackupState,
	}
	if !p.LastUsedAt.IsZero() {
		v.LastUseTime = timestamppb.New(p.LastUsedAt)
	}
	return v
}

func passkeyID(raw string) ([]byte, error) {
	id, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(id) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid passkey id"))
	}
	return id, nil
}

func (h *PasskeyHandler) fail(ctx context.Context, op string, err error) error {
	switch {
	case errors.Is(err, access.ErrStepUpRequired):
		return stepUpRequired()
	case errors.Is(err, passkey.ErrUnavailable):
		return connect.NewError(connect.CodeFailedPrecondition, passkey.ErrUnavailable)
	case errors.Is(err, passkey.ErrTooMany):
		return connect.NewError(connect.CodeResourceExhausted, passkey.ErrTooMany)
	case errors.Is(err, passkey.ErrInvalidName):
		return connect.NewError(connect.CodeInvalidArgument, passkey.ErrInvalidName)
	case errors.Is(err, passkey.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, passkey.ErrNotFound)
	case errors.Is(err, passkey.ErrFailed):
		return connect.NewError(connect.CodePermissionDenied, passkey.ErrFailed)
	}
	return internalError(ctx, h.logger, op, err)
}

func currentSession(ctx context.Context) (session.Session, error) {
	s, ok := sessionFrom(ctx)
	if !ok {
		return session.Session{}, connect.NewError(connect.CodeUnauthenticated, errUnauthenticated)
	}
	return s, nil
}

// ListPasskeys lists the caller's passkeys.
func (h *PasskeyHandler) ListPasskeys(ctx context.Context, _ *connect.Request[webv1.ListPasskeysRequest]) (*connect.Response[webv1.ListPasskeysResponse], error) {
	s, err := currentSession(ctx)
	if err != nil {
		return nil, err
	}
	keys, err := h.svc.List(ctx, s.User)
	if err != nil {
		return nil, h.fail(ctx, "list passkeys", err)
	}
	res := &webv1.ListPasskeysResponse{}
	for _, k := range keys {
		res.Passkeys = append(res.Passkeys, passkeyView(k))
	}
	return connect.NewResponse(res), nil
}

// BeginPasskeyRegistration starts adding a passkey.
func (h *PasskeyHandler) BeginPasskeyRegistration(ctx context.Context, req *connect.Request[webv1.BeginPasskeyRegistrationRequest]) (*connect.Response[webv1.BeginPasskeyRegistrationResponse], error) {
	s, err := currentSession(ctx)
	if err != nil {
		return nil, err
	}
	b, err := h.svc.BeginRegistration(ctx, s, req.Msg.GetName())
	if err != nil {
		return nil, h.fail(ctx, "begin passkey registration", err)
	}
	return connect.NewResponse(&webv1.BeginPasskeyRegistrationResponse{CeremonyId: b.CeremonyID, OptionsJson: string(b.Options)}), nil
}

// FinishPasskeyRegistration stores the new passkey.
func (h *PasskeyHandler) FinishPasskeyRegistration(ctx context.Context, req *connect.Request[webv1.FinishPasskeyRegistrationRequest]) (*connect.Response[webv1.FinishPasskeyRegistrationResponse], error) {
	s, err := currentSession(ctx)
	if err != nil {
		return nil, err
	}
	p, err := h.svc.FinishRegistration(ctx, s, req.Msg.GetCeremonyId(), []byte(req.Msg.GetCredentialJson()))
	if err != nil {
		return nil, h.fail(ctx, "finish passkey registration", err)
	}
	return connect.NewResponse(&webv1.FinishPasskeyRegistrationResponse{Passkey: passkeyView(p)}), nil
}

// RenamePasskey names a passkey anew.
func (h *PasskeyHandler) RenamePasskey(ctx context.Context, req *connect.Request[webv1.RenamePasskeyRequest]) (*connect.Response[webv1.RenamePasskeyResponse], error) {
	s, err := currentSession(ctx)
	if err != nil {
		return nil, err
	}
	id, err := passkeyID(req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	if err := h.svc.Rename(ctx, s.User, id, req.Msg.GetName()); err != nil {
		return nil, h.fail(ctx, "rename passkey", err)
	}
	return connect.NewResponse(&webv1.RenamePasskeyResponse{}), nil
}

// DeletePasskey removes a passkey.
func (h *PasskeyHandler) DeletePasskey(ctx context.Context, req *connect.Request[webv1.DeletePasskeyRequest]) (*connect.Response[webv1.DeletePasskeyResponse], error) {
	s, err := currentSession(ctx)
	if err != nil {
		return nil, err
	}
	id, err := passkeyID(req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	if err := h.svc.Delete(ctx, s, id); err != nil {
		return nil, h.fail(ctx, "delete passkey", err)
	}
	return connect.NewResponse(&webv1.DeletePasskeyResponse{}), nil
}

// WithPasskeys turns on the assertions with passkeys in AuthService (M3-23).
func (h *AuthHandler) WithPasskeys(svc *passkey.Service) *AuthHandler {
	h.passkeys = svc
	return h
}

func passkeyPurpose(p webv1.PasskeyPurpose) (passkey.Purpose, error) {
	switch p {
	case webv1.PasskeyPurpose_PASSKEY_PURPOSE_LOGIN:
		return passkey.PurposeLogin, nil
	case webv1.PasskeyPurpose_PASSKEY_PURPOSE_SECOND_FACTOR:
		return passkey.PurposeSecondFactor, nil
	case webv1.PasskeyPurpose_PASSKEY_PURPOSE_STEP_UP:
		return passkey.PurposeStepUp, nil
	case webv1.PasskeyPurpose_PASSKEY_PURPOSE_UNSPECIFIED:
	}
	return 0, connect.NewError(connect.CodeInvalidArgument, errors.New("unknown purpose"))
}

func passkeyCookie(value string, maxAge time.Duration) *http.Cookie {
	c := &http.Cookie{
		Name: PasskeyCookie, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
		MaxAge: int(maxAge.Seconds()),
	}
	if maxAge < 0 {
		c.MaxAge = -1
	}
	return c
}

func (h *AuthHandler) passkeyFailure(ctx context.Context, op string, purpose passkey.Purpose, err error) error {
	switch {
	case errors.Is(err, passkey.ErrTooManyAttempts):
		return connect.NewError(connect.CodeResourceExhausted, passkey.ErrTooManyAttempts)
	case errors.Is(err, passkey.ErrNotSignedIn):
		return connect.NewError(connect.CodeUnauthenticated, session.ErrInvalid)
	case errors.Is(err, passkey.ErrUnavailable):
		return connect.NewError(connect.CodeFailedPrecondition, passkey.ErrUnavailable)
	case errors.Is(err, passkey.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, passkey.ErrNotFound)
	case errors.Is(err, passkey.ErrFailed) && purpose == passkey.PurposeLogin:
		return connect.NewError(connect.CodeUnauthenticated, passkey.ErrFailed)
	case errors.Is(err, passkey.ErrFailed):
		return connect.NewError(connect.CodePermissionDenied, passkey.ErrFailed)
	}
	return h.internal(ctx, op, err)
}

// BeginPasskey starts an assertion with a passkey.
func (h *AuthHandler) BeginPasskey(ctx context.Context, req *connect.Request[webv1.BeginPasskeyRequest]) (*connect.Response[webv1.BeginPasskeyResponse], error) {
	purpose, err := passkeyPurpose(req.Msg.GetPurpose())
	if err != nil {
		return nil, err
	}
	if !h.passkeys.Available() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, passkey.ErrUnavailable)
	}
	token, _ := tokenFrom(req.Header())
	b, err := h.passkeys.Begin(ctx, purpose, token, ClientIPFrom(ctx))
	if err != nil {
		return nil, h.passkeyFailure(ctx, "begin passkey", purpose, err)
	}
	res := connect.NewResponse(&webv1.BeginPasskeyResponse{CeremonyId: b.CeremonyID, OptionsJson: string(b.Options)})
	if b.Binding != "" {
		res.Header().Add("Set-Cookie", passkeyCookie(b.Binding, passkey.CeremonyLifetime).String())
	}
	return res, nil
}

// FinishPasskey completes an assertion with a passkey.
func (h *AuthHandler) FinishPasskey(ctx context.Context, req *connect.Request[webv1.FinishPasskeyRequest]) (*connect.Response[webv1.FinishPasskeyResponse], error) {
	purpose, err := passkeyPurpose(req.Msg.GetPurpose())
	if err != nil {
		return nil, err
	}
	if !h.passkeys.Available() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, passkey.ErrUnavailable)
	}
	token, _ := tokenFrom(req.Header())
	binding := ""
	if c, err := (&http.Request{Header: http.Header{"Cookie": req.Header().Values("Cookie")}}).Cookie(PasskeyCookie); err == nil {
		binding = c.Value
	}
	result, err := h.passkeys.Finish(ctx, purpose, req.Msg.GetCeremonyId(), binding, token, []byte(req.Msg.GetCredentialJson()), ClientIPFrom(ctx))
	if err != nil {
		return nil, h.passkeyFailure(ctx, "finish passkey", purpose, err)
	}
	view, err := h.view(ctx, result.Session)
	if err != nil {
		return nil, h.internal(ctx, "session view", err)
	}
	res := connect.NewResponse(&webv1.FinishPasskeyResponse{Session: view})
	if purpose == passkey.PurposeLogin {
		res.Header().Add("Set-Cookie", passkeyCookie("", -1).String())
	}
	if result.Token != "" {
		remaining := time.Until(result.Session.CreatedAt.Add(session.AbsoluteTimeout))
		res.Header().Add("Set-Cookie", sessionCookie(result.Token, max(remaining, time.Second)).String())
	}
	return res, nil
}
