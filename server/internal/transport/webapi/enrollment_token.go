package webapi

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/enrollment"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// EnrollmentTokenHandler implements sermon.web.v1.EnrollmentTokenService (behind the Authenticator).
type EnrollmentTokenHandler struct {
	svc    *enrollment.Service
	logger *slog.Logger
}

var _ webv1connect.EnrollmentTokenServiceHandler = (*EnrollmentTokenHandler)(nil)

// NewEnrollmentTokenHandler returns the EnrollmentTokenService handler.
func NewEnrollmentTokenHandler(svc *enrollment.Service, logger *slog.Logger) *EnrollmentTokenHandler {
	return &EnrollmentTokenHandler{svc: svc, logger: logger}
}

// CreateEnrollmentToken issues a token.
func (h *EnrollmentTokenHandler) CreateEnrollmentToken(ctx context.Context, req *connect.Request[webv1.CreateEnrollmentTokenRequest]) (*connect.Response[webv1.CreateEnrollmentTokenResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	group, err := parseID(req.Msg.GetGroupId())
	if err != nil {
		return nil, err
	}
	ttl := time.Duration(req.Msg.GetTtlSeconds()) * time.Second
	secret, tok, err := h.svc.Create(ctx, c, tenant.GroupID(group), ttl, int(req.Msg.GetMaxUses()))
	if err != nil {
		return nil, h.fail(ctx, "create enrollment token", err)
	}
	return connect.NewResponse(&webv1.CreateEnrollmentTokenResponse{Token: tokenView(tok), Secret: secret}), nil
}

// ListEnrollmentTokens lists open tokens.
func (h *EnrollmentTokenHandler) ListEnrollmentTokens(ctx context.Context, _ *connect.Request[webv1.ListEnrollmentTokensRequest]) (*connect.Response[webv1.ListEnrollmentTokensResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	tokens, err := h.svc.List(ctx, c)
	if err != nil {
		return nil, h.fail(ctx, "list enrollment tokens", err)
	}
	res := &webv1.ListEnrollmentTokensResponse{}
	for _, t := range tokens {
		res.Tokens = append(res.Tokens, tokenView(t))
	}
	return connect.NewResponse(res), nil
}

// RevokeEnrollmentToken revokes a token.
func (h *EnrollmentTokenHandler) RevokeEnrollmentToken(ctx context.Context, req *connect.Request[webv1.RevokeEnrollmentTokenRequest]) (*connect.Response[webv1.RevokeEnrollmentTokenResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.Msg.GetTokenId())
	if err != nil {
		return nil, err
	}
	if err := h.svc.Revoke(ctx, c, enrollment.TokenID(id)); err != nil {
		return nil, h.fail(ctx, "revoke enrollment token", err)
	}
	return connect.NewResponse(&webv1.RevokeEnrollmentTokenResponse{}), nil
}

func (h *EnrollmentTokenHandler) fail(ctx context.Context, op string, err error) error {
	switch {
	case errors.Is(err, access.ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, access.ErrForbidden)
	case errors.Is(err, access.ErrStepUpRequired):
		return stepUpRequired()
	case errors.Is(err, access.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, access.ErrNotFound)
	case errors.Is(err, enrollment.ErrInvalidLimits):
		return connect.NewError(connect.CodeInvalidArgument, enrollment.ErrInvalidLimits)
	}
	return internalError(ctx, h.logger, op, err)
}

func tokenView(t enrollment.Token) *webv1.EnrollmentToken {
	v := &webv1.EnrollmentToken{
		Id: t.ID.String(), GroupId: t.Group.String(), CreateTime: timestamppb.New(t.CreatedAt), ExpireTime: timestamppb.New(t.ExpiresAt),
		MaxUses: int32(t.MaxUses), Uses: int32(t.Uses), //nolint:gosec // G115: bounded by MaxUses (1000)
	}
	if !t.CreatedBy.IsZero() {
		v.CreatedBy = t.CreatedBy.String()
	}
	return v
}
