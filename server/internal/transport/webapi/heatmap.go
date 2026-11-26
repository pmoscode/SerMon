package webapi

import (
	"context"
	"errors"
	"log/slog"
	"math"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// HeatmapHandler implements sermon.web.v1.HeatmapService (behind the Authenticator, M4-27).
type HeatmapHandler struct {
	fleet  *fleet.Service
	logger *slog.Logger
}

var _ webv1connect.HeatmapServiceHandler = (*HeatmapHandler)(nil)

// NewHeatmapHandler returns the HeatmapService handler.
func NewHeatmapHandler(svc *fleet.Service, logger *slog.Logger) *HeatmapHandler {
	return &HeatmapHandler{fleet: svc, logger: logger}
}

var heatmapKinds = map[webv1.HeatmapKind]fleet.HeatmapKind{
	webv1.HeatmapKind_HEATMAP_KIND_CPU:     fleet.HeatmapCPU,
	webv1.HeatmapKind_HEATMAP_KIND_MEMORY:  fleet.HeatmapMemory,
	webv1.HeatmapKind_HEATMAP_KIND_IO:      fleet.HeatmapIO,
	webv1.HeatmapKind_HEATMAP_KIND_ANOMALY: fleet.HeatmapAnomaly,
}

// GetHeatmap returns the heatmap of the visible nodes.
func (h *HeatmapHandler) GetHeatmap(ctx context.Context, req *connect.Request[webv1.GetHeatmapRequest]) (*connect.Response[webv1.GetHeatmapResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := heavyRead(ctx)
	defer cancel()
	heatmap, err := h.fleet.Heatmap(ctx, c, heatmapKinds[req.Msg.GetKind()], req.Msg.GetFrom().AsTime(), req.Msg.GetTo().AsTime())
	if errors.Is(err, fleet.ErrInvalidHeatmap) {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err != nil {
		return nil, readFailure(ctx, h.logger, "heatmap", err)
	}
	res := &webv1.GetHeatmapResponse{
		Start: timestamppb.New(heatmap.Start), Step: durationpb.New(heatmap.Step), Columns: int32(heatmap.Columns), //nolint:gosec // at most 200
		Truncated: heatmap.Truncated,
	}
	for _, r := range heatmap.Rows {
		values := make([]float32, len(r.Cells))
		for i, v := range r.Cells {
			// Three decimals are more than a colour shows; they keep the answer small.
			values[i] = float32(math.Round(v*1000) / 1000)
		}
		res.Rows = append(res.Rows, &webv1.HeatmapRow{NodeId: r.Node.String(), Hostname: r.Hostname, GroupName: r.Group, Values: values})
	}
	return connect.NewResponse(res), nil
}
