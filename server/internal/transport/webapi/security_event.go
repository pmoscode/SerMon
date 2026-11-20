package webapi

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/secevents"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/secevent"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// SecurityEventHandler implements sermon.web.v1.SecurityEventService (behind the Authenticator).
type SecurityEventHandler struct {
	events *secevents.Service
	logger *slog.Logger
}

var _ webv1connect.SecurityEventServiceHandler = (*SecurityEventHandler)(nil)

// NewSecurityEventHandler returns the SecurityEventService handler.
func NewSecurityEventHandler(events *secevents.Service, logger *slog.Logger) *SecurityEventHandler {
	return &SecurityEventHandler{events: events, logger: logger}
}

// ListSecurityEvents returns a page of the caller's visible security events.
func (h *SecurityEventHandler) ListSecurityEvents(ctx context.Context, req *connect.Request[webv1.ListSecurityEventsRequest]) (*connect.Response[webv1.ListSecurityEventsResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	m := req.Msg
	r := secevents.Request{
		Filter:   secevents.Filter{From: m.GetFrom().AsTime(), To: m.GetTo().AsTime(), User: m.User},
		PageSize: int(m.GetPageSize()), PageToken: m.GetPageToken(),
	}
	for _, raw := range m.GetNodeIds() {
		id, err := parseID(raw)
		if err != nil {
			return nil, err
		}
		r.Nodes = append(r.Nodes, identity.NodeID(id))
	}
	for _, k := range m.GetKinds() {
		r.Kinds = append(r.Kinds, secevent.Kind(k))
	}
	if m.GetNetwork() != "" {
		network, err := secevents.ParseNetwork(m.GetNetwork())
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		r.Network = &network
	}
	page, err := h.events.List(ctx, c, r)
	switch {
	case errors.Is(err, access.ErrForbidden):
		return nil, connect.NewError(connect.CodePermissionDenied, access.ErrForbidden)
	case errors.Is(err, secevent.ErrInvalid):
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	case err != nil:
		return nil, internalError(ctx, h.logger, "list security events", err)
	}
	res := &webv1.ListSecurityEventsResponse{NextPageToken: page.Next, Events: make([]*webv1.SecurityEvent, len(page.Events))}
	for i, e := range page.Events {
		res.Events[i] = securityEvent(e)
	}
	return connect.NewResponse(res), nil
}

func securityEvent(e secevent.Event) *webv1.SecurityEvent {
	d := e.Details
	out := &webv1.SecurityEvent{
		Id: e.ID, NodeId: e.Node.String(), OccurredAt: timestamppb.New(e.OccurredAt), ReceivedAt: timestamppb.New(e.ReceivedAt),
		Kind: string(e.Kind), User: e.User,
		Details: &webv1.SecurityEventDetails{
			Method: d.Method, InvalidUser: d.InvalidUser, KeyType: d.KeyType, KeyFingerprint: d.KeyFingerprint, Tty: d.TTY,
			Target: d.Target, Command: d.Command, Uid: d.UID, Pid: d.PID, Process: d.Process, PreviousBootId: d.PreviousBootID,
		},
	}
	if d.UptimeBefore != nil {
		out.Details.UptimeBefore = durationpb.New(*d.UptimeBefore)
	}
	if r := e.Remote; r != nil {
		out.RemoteHost, out.RemotePort = r.Host, uint32(r.Port)
		if r.IP.IsValid() {
			out.RemoteIp = r.IP.String()
		}
	}
	return out
}
