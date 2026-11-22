package webapi

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pmoscode/SerMon/server/internal/app/auth/password"
	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
	"github.com/pmoscode/SerMon/server/internal/app/invitation"
	"github.com/pmoscode/SerMon/server/internal/domain/quota"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// InvitationHandler implements sermon.web.v1.InvitationService (link token as credential,
// rate limited per client IP; JoinOrganization additionally needs the session cookie).
type InvitationHandler struct {
	svc      *invitation.Service
	sessions *session.Manager
	limiter  RateLimiter
	logger   *slog.Logger
}

var _ webv1connect.InvitationServiceHandler = (*InvitationHandler)(nil)

var errTooManyInvitationAttempts = errors.New("too many attempts, please wait a minute")

// NewInvitationHandler returns the InvitationService handler.
func NewInvitationHandler(svc *invitation.Service, sessions *session.Manager, limiter RateLimiter, logger *slog.Logger) *InvitationHandler {
	return &InvitationHandler{svc: svc, sessions: sessions, limiter: limiter, logger: logger}
}

// GetInvitation describes the invitation of a link.
func (h *InvitationHandler) GetInvitation(ctx context.Context, req *connect.Request[webv1.GetInvitationRequest]) (*connect.Response[webv1.GetInvitationResponse], error) {
	if !h.limiter.Allow(ClientIPFrom(ctx)) {
		return nil, connect.NewError(connect.CodeResourceExhausted, errTooManyInvitationAttempts)
	}
	p, err := h.svc.Inspect(ctx, req.Msg.GetToken())
	if err != nil {
		return nil, h.fail(ctx, "inspect invitation", err)
	}
	return connect.NewResponse(&webv1.GetInvitationResponse{
		OrganizationName: p.OrgName, Email: p.Email, Role: roleView(p.Role), ExpireTime: timestamppb.New(p.ExpiresAt),
	}), nil
}

// AcceptInvitation creates the account for the invited address.
func (h *InvitationHandler) AcceptInvitation(ctx context.Context, req *connect.Request[webv1.AcceptInvitationRequest]) (*connect.Response[webv1.AcceptInvitationResponse], error) {
	if !h.limiter.Allow(ClientIPFrom(ctx)) {
		return nil, connect.NewError(connect.CodeResourceExhausted, errTooManyInvitationAttempts)
	}
	p, err := h.svc.AcceptNewAccount(ctx, req.Msg.GetToken(), req.Msg.GetDisplayName(), req.Msg.GetPassword())
	if err != nil {
		return nil, h.fail(ctx, "accept invitation", err)
	}
	return connect.NewResponse(&webv1.AcceptInvitationResponse{OrganizationName: p.OrgName}), nil
}

// JoinOrganization adds the logged-in user to the inviting organization.
func (h *InvitationHandler) JoinOrganization(ctx context.Context, req *connect.Request[webv1.JoinOrganizationRequest]) (*connect.Response[webv1.JoinOrganizationResponse], error) {
	if !h.limiter.Allow(ClientIPFrom(ctx)) {
		return nil, connect.NewError(connect.CodeResourceExhausted, errTooManyInvitationAttempts)
	}
	token, ok := tokenFrom(req.Header())
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errUnauthenticated)
	}
	s, err := h.sessions.Validate(ctx, token)
	if errors.Is(err, session.ErrInvalid) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errUnauthenticated)
	}
	if err != nil {
		return nil, internalError(ctx, h.logger, "validate session", err)
	}
	p, err := h.svc.Join(ctx, req.Msg.GetToken(), s.User)
	if err != nil {
		return nil, h.fail(ctx, "join organization", err)
	}
	return connect.NewResponse(&webv1.JoinOrganizationResponse{OrganizationId: p.Org.String(), OrganizationName: p.OrgName}), nil
}

func (h *InvitationHandler) fail(ctx context.Context, op string, err error) error {
	if errors.Is(err, invitation.ErrInvalidInvitation) {
		return connect.NewError(connect.CodeUnauthenticated, invitation.ErrInvalidInvitation)
	}
	if errors.Is(err, quota.ErrExceeded) {
		return quotaExceeded(err)
	}
	// Validation errors carry user-facing messages (no secrets, no internal details).
	for _, userFacing := range []error{
		invitation.ErrInvalidName,
		password.ErrTooShort, password.ErrTooLong, password.ErrCommon, password.ErrContainsContext,
	} {
		if errors.Is(err, userFacing) {
			return connect.NewError(connect.CodeInvalidArgument, userFacing)
		}
	}
	return internalError(ctx, h.logger, op, err)
}
