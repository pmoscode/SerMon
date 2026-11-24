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

// filterOf reads the fields of a filter; INVALID_ARGUMENT for a bad node ID or network.
func filterOf(from, to *timestamppb.Timestamp, nodeIDs, kinds []string, user *string, network string, newOnly bool) (secevents.Filter, error) {
	f := secevents.Filter{From: from.AsTime(), To: to.AsTime(), User: user, NewSourcesOnly: newOnly}
	nodes, err := nodesOf(nodeIDs)
	if err != nil {
		return secevents.Filter{}, err
	}
	f.Nodes = nodes
	for _, k := range kinds {
		f.Kinds = append(f.Kinds, secevent.Kind(k))
	}
	if network != "" {
		n, err := secevents.ParseNetwork(network)
		if err != nil {
			return secevents.Filter{}, connect.NewError(connect.CodeInvalidArgument, err)
		}
		f.Network = &n
	}
	return f, nil
}

func nodesOf(ids []string) ([]identity.NodeID, error) {
	var out []identity.NodeID
	for _, raw := range ids {
		id, err := parseID(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, identity.NodeID(id))
	}
	return out, nil
}

func (h *SecurityEventHandler) failure(ctx context.Context, what string, err error) error {
	switch {
	case errors.Is(err, access.ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, access.ErrForbidden)
	case errors.Is(err, secevent.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return internalError(ctx, h.logger, what, err)
}

// ListSecurityEvents returns a page of the caller's visible security events.
func (h *SecurityEventHandler) ListSecurityEvents(ctx context.Context, req *connect.Request[webv1.ListSecurityEventsRequest]) (*connect.Response[webv1.ListSecurityEventsResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	m := req.Msg
	f, err := filterOf(m.GetFrom(), m.GetTo(), m.GetNodeIds(), m.GetKinds(), m.User, m.GetNetwork(), m.GetNewSourcesOnly())
	if err != nil {
		return nil, err
	}
	page, err := h.events.List(ctx, c, secevents.Request{Filter: f, PageSize: int(m.GetPageSize()), PageToken: m.GetPageToken()})
	if err != nil {
		return nil, h.failure(ctx, "list security events", err)
	}
	res := &webv1.ListSecurityEventsResponse{NextPageToken: page.Next, Events: make([]*webv1.SecurityEvent, len(page.Events))}
	for i, e := range page.Events {
		res.Events[i] = securityEvent(e)
	}
	return connect.NewResponse(res), nil
}

// GetSecurityOverview returns the evaluations of a range (M3-16).
func (h *SecurityEventHandler) GetSecurityOverview(ctx context.Context, req *connect.Request[webv1.GetSecurityOverviewRequest]) (*connect.Response[webv1.GetSecurityOverviewResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	m := req.Msg
	nodes, err := nodesOf(m.GetNodeIds())
	if err != nil {
		return nil, err
	}
	o, err := h.events.Overview(ctx, c, secevents.OverviewRequest{From: m.GetFrom().AsTime(), To: m.GetTo().AsTime(), Nodes: nodes})
	if err != nil {
		return nil, h.failure(ctx, "security overview", err)
	}
	t := o.Totals
	res := &webv1.GetSecurityOverviewResponse{
		Step: durationpb.New(o.Step),
		Totals: &webv1.SecurityEventTotals{
			FailedLogins: count(t.FailedLogins), Logins: count(t.Logins), NewSourceLogins: count(t.NewSourceLogins),
			RootLogins: count(t.RootLogins), SudoFailures: count(t.SudoFailures),
		},
		TopIps: counts(o.TopIPs), TopUsers: counts(o.TopUsers),
	}
	for _, s := range o.FailedLogins {
		series := &webv1.NodeEventSeries{NodeId: s.Node.String(), Points: make([]*webv1.EventCountPoint, len(s.Points))}
		for i, p := range s.Points {
			series.Points[i] = &webv1.EventCountPoint{At: timestamppb.New(p.At), Count: count(p.Count)}
		}
		res.FailedLogins = append(res.FailedLogins, series)
	}
	return connect.NewResponse(res), nil
}

func count(n int64) uint64 { return uint64(max(n, 0)) }

func counts(cs []secevents.Count) []*webv1.SecurityEventCount {
	out := make([]*webv1.SecurityEventCount, len(cs))
	for i, c := range cs {
		out[i] = &webv1.SecurityEventCount{
			Value: c.Value, Count: count(c.Count), Nodes: uint32(min(max(c.Nodes, 0), 1<<31)), LastAt: timestamppb.New(c.Last),
			Unverified: count(c.Unverified),
		}
	}
	return out
}

// ExportSecurityEvents returns the events of a filter as CSV (M3-16).
func (h *SecurityEventHandler) ExportSecurityEvents(ctx context.Context, req *connect.Request[webv1.ExportSecurityEventsRequest]) (*connect.Response[webv1.ExportSecurityEventsResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	m := req.Msg.GetFilter()
	f, err := filterOf(m.GetFrom(), m.GetTo(), m.GetNodeIds(), m.GetKinds(), m.User, m.GetNetwork(), m.GetNewSourcesOnly())
	if err != nil {
		return nil, err
	}
	e, err := h.events.Export(ctx, c, f)
	if err != nil {
		return nil, h.failure(ctx, "export security events", err)
	}
	return connect.NewResponse(&webv1.ExportSecurityEventsResponse{
		FileName: e.FileName, Content: e.Content, Rows: uint32(min(max(e.Rows, 0), 1<<31)), Truncated: e.Truncated,
	}), nil
}

func securityEvent(e secevent.Event) *webv1.SecurityEvent {
	d := e.Details
	out := &webv1.SecurityEvent{
		Id: e.ID, NodeId: e.Node.String(), OccurredAt: timestamppb.New(e.OccurredAt), ReceivedAt: timestamppb.New(e.ReceivedAt),
		Kind: string(e.Kind), User: e.User, Unverified: e.Unverified, NewSource: e.NewSource,
		Details: &webv1.SecurityEventDetails{
			Method: d.Method, InvalidUser: d.InvalidUser, KeyType: d.KeyType, KeyFingerprint: d.KeyFingerprint, Tty: d.TTY,
			Target: d.Target, Command: d.Command, Uid: d.UID, Pid: d.PID, Process: d.Process, PreviousBootId: d.PreviousBootID,
			TokenId: d.TokenID, Reason: d.Reason, PreviousIp: d.PreviousIP,
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
