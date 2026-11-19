package webapi

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"

	"github.com/pmoscode/SerMon/server/internal/app/auth/password"
	"github.com/pmoscode/SerMon/server/internal/app/setup"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// RateLimiter limits attempts per key (ratelimit.Limiter).
type RateLimiter interface {
	Allow(key string) bool
}

// SetupHandler implements sermon.web.v1.SetupService (no session; rate limited).
type SetupHandler struct {
	svc     *setup.Service
	limiter RateLimiter
	logger  *slog.Logger
}

var _ webv1connect.SetupServiceHandler = (*SetupHandler)(nil)

var errTooManySetupAttempts = errors.New("too many attempts, please wait a minute")

// NewSetupHandler returns the SetupService handler.
func NewSetupHandler(svc *setup.Service, limiter RateLimiter, logger *slog.Logger) *SetupHandler {
	return &SetupHandler{svc: svc, limiter: limiter, logger: logger}
}

// GetSetupStatus reports whether the setup is open.
func (h *SetupHandler) GetSetupStatus(ctx context.Context, _ *connect.Request[webv1.GetSetupStatusRequest]) (*connect.Response[webv1.GetSetupStatusResponse], error) {
	required, err := h.svc.Required(ctx)
	if err != nil {
		return nil, internalError(ctx, h.logger, "setup status", err)
	}
	return connect.NewResponse(&webv1.GetSetupStatusResponse{SetupRequired: required}), nil
}

// CompleteSetup creates the first superadmin.
func (h *SetupHandler) CompleteSetup(ctx context.Context, req *connect.Request[webv1.CompleteSetupRequest]) (*connect.Response[webv1.CompleteSetupResponse], error) {
	if !h.limiter.Allow(ClientIPFrom(ctx)) {
		return nil, connect.NewError(connect.CodeResourceExhausted, errTooManySetupAttempts)
	}
	m := req.Msg
	err := h.svc.Complete(ctx, setup.Request{
		Token: m.GetToken(), Email: m.GetEmail(), DisplayName: m.GetDisplayName(),
		Password: m.GetPassword(), OrganizationName: m.GetOrganizationName(),
	})
	switch {
	case err == nil:
		h.logger.InfoContext(ctx, "first installation completed")
		return connect.NewResponse(&webv1.CompleteSetupResponse{}), nil
	case errors.Is(err, setup.ErrInvalidToken):
		return nil, connect.NewError(connect.CodeUnauthenticated, setup.ErrInvalidToken)
	}
	// Validation errors carry user-facing messages (no secrets, no internal details).
	for _, policy := range []error{
		setup.ErrInvalidEmail, setup.ErrInvalidName,
		password.ErrTooShort, password.ErrTooLong, password.ErrCommon, password.ErrContainsContext,
	} {
		if errors.Is(err, policy) {
			return nil, connect.NewError(connect.CodeInvalidArgument, policy)
		}
	}
	return nil, internalError(ctx, h.logger, "complete setup", err)
}
