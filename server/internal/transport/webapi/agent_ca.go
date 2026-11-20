package webapi

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/agentca"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// AgentCAAdmin rolls the agent CA over (agentca.Service).
type AgentCAAdmin interface {
	Status(ctx context.Context, c access.Caller) ([]agentca.AuthorityState, error)
	StartRollover(ctx context.Context, c access.Caller) error
	Retire(ctx context.Context, c access.Caller, id int32) error
}

// AgentCAHandler implements sermon.web.v1.AgentCAService (behind the Authenticator).
type AgentCAHandler struct {
	ca     AgentCAAdmin
	logger *slog.Logger
}

var _ webv1connect.AgentCAServiceHandler = (*AgentCAHandler)(nil)

// NewAgentCAHandler returns the AgentCAService handler.
func NewAgentCAHandler(ca AgentCAAdmin, logger *slog.Logger) *AgentCAHandler {
	return &AgentCAHandler{ca: ca, logger: logger}
}

// GetAgentCAStatus lists the CAs.
func (h *AgentCAHandler) GetAgentCAStatus(ctx context.Context, _ *connect.Request[webv1.GetAgentCAStatusRequest]) (*connect.Response[webv1.GetAgentCAStatusResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	states, err := h.ca.Status(ctx, c)
	if err != nil {
		return nil, h.fail(ctx, "agent CA status", err)
	}
	res := &webv1.GetAgentCAStatusResponse{}
	for _, st := range states {
		v := &webv1.AgentCA{
			Id: st.ID, Issuing: st.Issuing, CreateTime: timestamppb.New(st.CreatedAt),
			RootFingerprint: st.RootFingerprint, CertificatesInUse: st.CertificatesInUse,
		}
		if st.RetiredAt != nil {
			v.RetireTime = timestamppb.New(*st.RetiredAt)
		}
		res.Authorities = append(res.Authorities, v)
	}
	return connect.NewResponse(res), nil
}

// StartAgentCARollover creates a new issuing CA.
func (h *AgentCAHandler) StartAgentCARollover(ctx context.Context, _ *connect.Request[webv1.StartAgentCARolloverRequest]) (*connect.Response[webv1.StartAgentCARolloverResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.ca.StartRollover(ctx, c); err != nil {
		return nil, h.fail(ctx, "start agent CA rollover", err)
	}
	h.logger.WarnContext(ctx, "agent CA rollover started", "by", c.Principal.User.String())
	return connect.NewResponse(&webv1.StartAgentCARolloverResponse{}), nil
}

// RetireAgentCA stops trusting a previous CA.
func (h *AgentCAHandler) RetireAgentCA(ctx context.Context, req *connect.Request[webv1.RetireAgentCARequest]) (*connect.Response[webv1.RetireAgentCAResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.ca.Retire(ctx, c, req.Msg.GetId()); err != nil {
		return nil, h.fail(ctx, "retire agent CA", err)
	}
	h.logger.WarnContext(ctx, "agent CA retired", "id", req.Msg.GetId(), "by", c.Principal.User.String())
	return connect.NewResponse(&webv1.RetireAgentCAResponse{}), nil
}

func (h *AgentCAHandler) fail(ctx context.Context, op string, err error) error {
	switch {
	case errors.Is(err, access.ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, access.ErrForbidden)
	case errors.Is(err, access.ErrStepUpRequired):
		return stepUpRequired()
	case errors.Is(err, agentca.ErrUnknownAuthority):
		return connect.NewError(connect.CodeNotFound, agentca.ErrUnknownAuthority)
	}
	for _, precondition := range []error{agentca.ErrRolloverInProgress, agentca.ErrAuthorityInUse, agentca.ErrIssuingAuthority} {
		if errors.Is(err, precondition) {
			return connect.NewError(connect.CodeFailedPrecondition, precondition)
		}
	}
	return internalError(ctx, h.logger, op, err)
}
