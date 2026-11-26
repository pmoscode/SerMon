package webapi

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	"github.com/pmoscode/SerMon/server/internal/domain/textsafe"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// CapacityHandler implements sermon.web.v1.CapacityService (behind the Authenticator, M4-26).
type CapacityHandler struct {
	fleet  *fleet.Service
	reads  *HeavyReads
	logger *slog.Logger
}

var _ webv1connect.CapacityServiceHandler = (*CapacityHandler)(nil)

// NewCapacityHandler returns the CapacityService handler; reads are the limits shared with the
// other dashboards (nil: its own).
func NewCapacityHandler(svc *fleet.Service, reads *HeavyReads, logger *slog.Logger) *CapacityHandler {
	return &CapacityHandler{fleet: svc, reads: reads.orNew(), logger: logger}
}

// GetCapacity returns the capacity planning of the visible nodes (M4-26).
func (h *CapacityHandler) GetCapacity(ctx context.Context, _ *connect.Request[webv1.GetCapacityRequest]) (*connect.Response[webv1.GetCapacityResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	ctx, release, err := h.reads.admit(ctx, c)
	if err != nil {
		return nil, err
	}
	defer release()
	capacity, err := h.fleet.Capacity(ctx, c)
	if err != nil {
		return nil, readFailure(ctx, h.logger, "capacity", err)
	}
	res := &webv1.GetCapacityResponse{Truncated: capacity.Truncated, TrendsTruncated: capacity.TrendsTruncated}
	for _, fs := range capacity.Filesystems {
		res.Filesystems = append(res.Filesystems, fleetFilesystemView(fs))
	}
	days := func(points []fleet.DailyPoint) []*webv1.DailyValue {
		out := make([]*webv1.DailyValue, len(points))
		for i, p := range points {
			out[i] = &webv1.DailyValue{Day: timestamppb.New(p.Day), Value: p.Value}
		}
		return out
	}
	for _, t := range capacity.Trends {
		res.Trends = append(res.Trends, &webv1.NodeTrend{
			NodeId: t.Node.String(), Hostname: t.Hostname, GroupName: t.Group, Cpu: days(t.CPU), Memory: days(t.Memory),
		})
	}
	return connect.NewResponse(res), nil
}

// fleetFilesystemView makes a file system of the fleet an answer; the mount point comes from the
// host and is shown as visible text only (M4-31, like the labels).
func fleetFilesystemView(fs fleet.NodeFilesystem) *webv1.FleetFilesystem {
	return &webv1.FleetFilesystem{
		NodeId: fs.Node.String(), Hostname: fs.Hostname, GroupName: fs.Group, Mountpoint: textsafe.Visible(fs.Mountpoint),
		SizeBytes: fs.Size, AvailBytes: fs.Avail, UsedRatio: fs.UsedRatio, Readonly: fs.Readonly, FullAt: optionalTime(fs.FullAt),
	}
}
