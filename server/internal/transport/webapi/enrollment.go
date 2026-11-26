package webapi

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pmoscode/SerMon/server/internal/app/enrollment"
	"github.com/pmoscode/SerMon/server/internal/domain/quota"
	"github.com/pmoscode/SerMon/server/internal/domain/textsafe"
	agentv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/agent/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/agent/v1/agentv1connect"
)

// AgentEnrollmentHandler implements sermon.agent.v1.EnrollmentService on the web listener
// (ADR 0009): no session and no origin check – the enrollment token is the credential; rate
// limited per client IP.
type AgentEnrollmentHandler struct {
	enroller *enrollment.Enroller
	limiter  RateLimiter
	logger   *slog.Logger
}

var _ agentv1connect.EnrollmentServiceHandler = (*AgentEnrollmentHandler)(nil)

var errTooManyEnrollments = errors.New("too many enrollment attempts, please wait a minute")

// NewAgentEnrollmentHandler returns the EnrollmentService handler.
func NewAgentEnrollmentHandler(enroller *enrollment.Enroller, limiter RateLimiter, logger *slog.Logger) *AgentEnrollmentHandler {
	return &AgentEnrollmentHandler{enroller: enroller, limiter: limiter, logger: logger}
}

// Enroll registers a new agent.
func (h *AgentEnrollmentHandler) Enroll(ctx context.Context, req *connect.Request[agentv1.EnrollRequest]) (*connect.Response[agentv1.EnrollResponse], error) {
	ip := ClientIPFrom(ctx)
	if !h.limiter.Allow(ip) {
		return nil, connect.NewError(connect.CodeResourceExhausted, errTooManyEnrollments)
	}
	f := req.Msg.GetHostFacts()
	// Chosen by the host and shown everywhere, already before its first session (review M4-30):
	// textsafe like the facts of a Hello.
	facts := enrollment.NodeFacts{
		Hostname: textsafe.Visible(f.GetHostname()), MachineID: f.GetMachineId(), OSID: textsafe.Visible(f.GetOsId()),
		OSVersion: textsafe.Visible(f.GetOsVersion()), KernelRelease: textsafe.Visible(f.GetKernelRelease()),
		Architecture: architecture(f.GetArchitecture()),
		CPUCount:     int(f.GetCpuCount()), MemoryBytes: int64(f.GetMemoryBytes()), //nolint:gosec // G115: physical memory fits int64
	}
	if addr, err := netip.ParseAddr(ip); err == nil {
		facts.ClientIP = addr.Unmap()
	}
	res, err := h.enroller.Enroll(ctx, req.Msg.GetToken(), req.Msg.GetCsr(), facts)
	switch {
	case errors.Is(err, enrollment.ErrInvalidToken):
		h.logger.WarnContext(ctx, "enrollment rejected: invalid token", "client_ip", ip)
		return nil, connect.NewError(connect.CodeUnauthenticated, enrollment.ErrInvalidToken)
	case errors.Is(err, enrollment.ErrInvalidCSR):
		return nil, connect.NewError(connect.CodeInvalidArgument, enrollment.ErrInvalidCSR)
	case errors.Is(err, enrollment.ErrMachineIDConflict):
		return nil, connect.NewError(connect.CodeAlreadyExists, enrollment.ErrMachineIDConflict)
	case errors.Is(err, quota.ErrExceeded):
		h.logger.WarnContext(ctx, "enrollment rejected: node quota reached", "client_ip", ip)
		return nil, quotaExceeded(err)
	case err != nil:
		return nil, internalError(ctx, h.logger, "enroll agent", err)
	}
	h.logger.InfoContext(ctx, "agent enrolled", "org", res.Org.String(), "node", res.Node.String(), "client_ip", ip)
	return connect.NewResponse(&agentv1.EnrollResponse{
		NodeId: res.Node.String(), Certificate: res.Certificate, Intermediates: [][]byte{res.Intermediate},
		TrustAnchor: res.TrustAnchor, TrustAnchors: res.TrustBundle, NotAfter: timestamppb.New(res.NotAfter),
	}), nil
}

func architecture(a agentv1.Architecture) string {
	switch a {
	case agentv1.Architecture_ARCHITECTURE_AMD64:
		return "amd64"
	case agentv1.Architecture_ARCHITECTURE_ARM64:
		return "arm64"
	case agentv1.Architecture_ARCHITECTURE_UNSPECIFIED:
	}
	return "" // rejected by protovalidate before
}
