package webapi

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/account"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// AccountHandler implements sermon.web.v1.AccountService (behind the session-only
// Authenticator: the own account, also without organization) and AccountAdminService (behind
// the full one: superadmins).
type AccountHandler struct {
	svc    *account.Service
	logger *slog.Logger
}

var (
	_ webv1connect.AccountServiceHandler      = (*AccountHandler)(nil)
	_ webv1connect.AccountAdminServiceHandler = (*AccountHandler)(nil)
)

// NewAccountHandler returns the handler.
func NewAccountHandler(svc *account.Service, logger *slog.Logger) *AccountHandler {
	return &AccountHandler{svc: svc, logger: logger}
}

// ownCaller is the session's user with its step-up; the account service needs no principal.
func ownCaller(ctx context.Context) (access.Caller, error) {
	s, err := currentSession(ctx)
	if err != nil {
		return access.Caller{}, err
	}
	return access.Caller{Principal: authz.Principal{User: s.User}, StepUpAt: s.StepUpAt}, nil
}

func (h *AccountHandler) fail(ctx context.Context, op string, err error) error {
	for _, known := range []struct {
		err  error
		code connect.Code
	}{
		{account.ErrInvalidName, connect.CodeInvalidArgument},
		{account.ErrInvalidEmail, connect.CodeInvalidArgument},
		{account.ErrUnchanged, connect.CodeInvalidArgument},
		{account.ErrEmailTaken, connect.CodeAlreadyExists},
		{account.ErrNoPendingChange, connect.CodeFailedPrecondition},
		{account.ErrWrongCode, connect.CodePermissionDenied},
		{account.ErrTooManyRequests, connect.CodeResourceExhausted},
		// The mail server's error stays in the log.
		{account.ErrMailFailed, connect.CodeUnavailable},
		{account.ErrSuperadmin, connect.CodeFailedPrecondition},
		{access.ErrLastAdmin, connect.CodeFailedPrecondition},
		{access.ErrNotFound, connect.CodeNotFound},
		{access.ErrForbidden, connect.CodePermissionDenied},
	} {
		if errors.Is(err, known.err) {
			if known.code == connect.CodeUnavailable {
				h.logger.WarnContext(ctx, "account: mail failed", "op", op, "error", err)
			}
			return connect.NewError(known.code, known.err)
		}
	}
	if errors.Is(err, access.ErrStepUpRequired) {
		return stepUpRequired()
	}
	return internalError(ctx, h.logger, op, err)
}

// GetAccount returns the caller's account.
func (h *AccountHandler) GetAccount(ctx context.Context, _ *connect.Request[webv1.GetAccountRequest]) (*connect.Response[webv1.GetAccountResponse], error) {
	c, err := ownCaller(ctx)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.Get(ctx, c.Principal.User)
	if err != nil {
		return nil, h.fail(ctx, "get account", err)
	}
	res := &webv1.GetAccountResponse{
		Email: v.Email, DisplayName: v.DisplayName, CreateTime: timestamppb.New(v.CreatedAt), EmailVerification: v.Verified,
	}
	if v.Pending != nil {
		res.PendingEmail, res.PendingExpireTime = v.Pending.NewEmail, timestamppb.New(v.Pending.ExpiresAt)
	}
	return connect.NewResponse(res), nil
}

// SetDisplayName changes the caller's name.
func (h *AccountHandler) SetDisplayName(ctx context.Context, req *connect.Request[webv1.SetDisplayNameRequest]) (*connect.Response[webv1.SetDisplayNameResponse], error) {
	c, err := ownCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.SetDisplayName(ctx, c, req.Msg.GetDisplayName()); err != nil {
		return nil, h.fail(ctx, "set display name", err)
	}
	return connect.NewResponse(&webv1.SetDisplayNameResponse{}), nil
}

// RequestEmailChange starts a change of the caller's address.
func (h *AccountHandler) RequestEmailChange(ctx context.Context, req *connect.Request[webv1.RequestEmailChangeRequest]) (*connect.Response[webv1.RequestEmailChangeResponse], error) {
	c, err := ownCaller(ctx)
	if err != nil {
		return nil, err
	}
	sent, err := h.svc.RequestEmailChange(ctx, c, req.Msg.GetEmail())
	if err != nil {
		return nil, h.fail(ctx, "request e-mail change", err)
	}
	return connect.NewResponse(&webv1.RequestEmailChangeResponse{CodeSent: sent}), nil
}

// ConfirmEmailChange applies the pending address.
func (h *AccountHandler) ConfirmEmailChange(ctx context.Context, req *connect.Request[webv1.ConfirmEmailChangeRequest]) (*connect.Response[webv1.ConfirmEmailChangeResponse], error) {
	c, err := ownCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.ConfirmEmailChange(ctx, c.Principal.User, req.Msg.GetCode()); err != nil {
		return nil, h.fail(ctx, "confirm e-mail change", err)
	}
	return connect.NewResponse(&webv1.ConfirmEmailChangeResponse{}), nil
}

// CancelEmailChange drops a pending change.
func (h *AccountHandler) CancelEmailChange(ctx context.Context, _ *connect.Request[webv1.CancelEmailChangeRequest]) (*connect.Response[webv1.CancelEmailChangeResponse], error) {
	c, err := ownCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.CancelEmailChange(ctx, c.Principal.User); err != nil {
		return nil, h.fail(ctx, "cancel e-mail change", err)
	}
	return connect.NewResponse(&webv1.CancelEmailChangeResponse{}), nil
}

// ExportAccountData returns the caller's data.
func (h *AccountHandler) ExportAccountData(ctx context.Context, _ *connect.Request[webv1.ExportAccountDataRequest]) (*connect.Response[webv1.ExportAccountDataResponse], error) {
	c, err := ownCaller(ctx)
	if err != nil {
		return nil, err
	}
	out, err := h.svc.Export(ctx, c)
	if err != nil {
		return nil, h.fail(ctx, "export account", err)
	}
	return connect.NewResponse(&webv1.ExportAccountDataResponse{Json: string(out)}), nil
}

// DeleteAccount deletes an account on request (superadmin).
func (h *AccountHandler) DeleteAccount(ctx context.Context, req *connect.Request[webv1.DeleteAccountRequest]) (*connect.Response[webv1.DeleteAccountResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	n, err := h.svc.Delete(ctx, c, req.Msg.GetEmail())
	if err != nil {
		return nil, h.fail(ctx, "delete account", err)
	}
	return connect.NewResponse(&webv1.DeleteAccountResponse{Memberships: int32(n)}), nil //nolint:gosec // G115: memberships of one account
}
