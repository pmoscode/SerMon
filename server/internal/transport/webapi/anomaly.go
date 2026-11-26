package webapi

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/anomalies"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/textsafe"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// AnomalyHandler serves AnomalyService (M4-23, ADR 0037).
type AnomalyHandler struct {
	anomalies *anomalies.Service
	logger    *slog.Logger
}

var _ webv1connect.AnomalyServiceHandler = (*AnomalyHandler)(nil)

// NewAnomalyHandler returns the handler.
func NewAnomalyHandler(svc *anomalies.Service, logger *slog.Logger) *AnomalyHandler {
	return &AnomalyHandler{anomalies: svc, logger: logger}
}

func (h *AnomalyHandler) failure(ctx context.Context, what string, err error) error {
	switch {
	case errors.Is(err, anomalies.ErrInvalidRange):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, errReadTooLong)
	case errors.Is(err, access.ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, access.ErrForbidden)
	case errors.Is(err, access.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, access.ErrNotFound)
	}
	return internalError(ctx, h.logger, what, err)
}

// labelsView makes labels the host chose safe to show (textsafe, M2-28).
func labelsView(labels map[string]string) map[string]string {
	out := make(map[string]string, len(labels))
	for k, v := range labels {
		out[k] = textsafe.Visible(v)
	}
	return out
}

// GetNodeAnomalies returns the band of a node.
func (h *AnomalyHandler) GetNodeAnomalies(ctx context.Context, req *connect.Request[webv1.GetNodeAnomaliesRequest]) (*connect.Response[webv1.GetNodeAnomaliesResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.Msg.GetNodeId())
	if err != nil {
		return nil, err
	}
	ctx, cancel := heavyRead(ctx)
	defer cancel()
	band, err := h.anomalies.Node(ctx, c, identity.NodeID(id), req.Msg.GetFrom().AsTime(), req.Msg.GetTo().AsTime())
	if err != nil {
		return nil, h.failure(ctx, "get node anomalies", err)
	}
	res := &webv1.GetNodeAnomaliesResponse{LearningSince: timestamppb.New(band.LearningSince), LearningDay: int32(band.LearningDay)} //nolint:gosec // 0 to 7
	for _, s := range band.Series {
		v := &webv1.SeriesAnomalies{Metric: s.Metric, Labels: labelsView(s.Labels), MaxScore: s.MaxScore}
		for _, iv := range s.Intervals {
			v.Intervals = append(v.Intervals, &webv1.AnomalyInterval{From: timestamppb.New(iv.From), To: timestamppb.New(iv.To)})
		}
		res.Series = append(res.Series, v)
	}
	return connect.NewResponse(res), nil
}

// ListUnusual returns "what was unusual?".
func (h *AnomalyHandler) ListUnusual(ctx context.Context, req *connect.Request[webv1.ListUnusualRequest]) (*connect.Response[webv1.ListUnusualResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	var node *identity.NodeID
	if raw := req.Msg.GetNodeId(); raw != "" {
		id, err := parseID(raw)
		if err != nil {
			return nil, err
		}
		n := identity.NodeID(id)
		node = &n
	}
	limit := int(req.Msg.GetLimit())
	if limit == 0 {
		limit = 20
	}
	ctx, cancel := heavyRead(ctx)
	defer cancel()
	found, err := h.anomalies.Unusual(ctx, c, node, req.Msg.GetFrom().AsTime(), req.Msg.GetTo().AsTime(), limit)
	if err != nil {
		return nil, h.failure(ctx, "list unusual series", err)
	}
	res := &webv1.ListUnusualResponse{}
	for _, u := range found {
		res.Series = append(res.Series, &webv1.UnusualSeries{
			NodeId: u.Node.String(), Metric: u.Metric, Labels: labelsView(u.Labels), MaxScore: u.MaxScore,
			Peak: timestamppb.New(u.Peak), AnomalousMinutes: int32(min(u.Anomalous, 1<<30)), //nolint:gosec // bounded
		})
	}
	return connect.NewResponse(res), nil
}
