package webapi

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/liveview"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// LiveViewHandler implements sermon.web.v1.LiveViewService (KONZEPT §9, M3-14).
type LiveViewHandler struct {
	svc     *liveview.Service
	streams *streamLimiter
	logger  *slog.Logger
}

var _ webv1connect.LiveViewServiceHandler = (*LiveViewHandler)(nil)

// NewLiveViewHandler returns the handler; live are the limits shared with the other live streams
// (nil: its own).
func NewLiveViewHandler(svc *liveview.Service, live *LiveStreams, logger *slog.Logger) *LiveViewHandler {
	return &LiveViewHandler{svc: svc, streams: live.orNew(), logger: logger}
}

// WatchProcesses streams the node's processes. The stream counts against the live stream limits;
// the service checks the session, the node and the switch again every 30 s.
func (h *LiveViewHandler) WatchProcesses(ctx context.Context, req *connect.Request[webv1.WatchProcessesRequest], stream *connect.ServerStream[webv1.WatchProcessesResponse]) error {
	c, err := callerFrom(ctx)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(req.Msg.GetNodeId())
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, errInvalidID)
	}
	s, _ := sessionFrom(ctx)
	key := streamKeyOf(s, c)
	if !h.streams.acquire(key) {
		return connect.NewError(connect.CodeResourceExhausted, errTooManyStreams)
	}
	defer h.streams.release(key)
	err = h.svc.Watch(ctx, c, identity.NodeID(id), func(f liveview.Frame) error {
		return stream.Send(frameMessage(f))
	}, recheckOf(ctx, c))
	var connectErr *connect.Error
	switch {
	case err == nil, errors.Is(err, context.Canceled), errors.Is(err, errScopeChanged):
		return nil
	case errors.As(err, &connectErr):
		return connectErr
	case errors.Is(err, access.ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, access.ErrForbidden)
	case errors.Is(err, access.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, access.ErrNotFound)
	}
	return internalError(ctx, h.logger, "watch processes", err)
}

func frameMessage(f liveview.Frame) *webv1.WatchProcessesResponse {
	switch {
	case f.Snapshot != nil:
		return &webv1.WatchProcessesResponse{Event: &webv1.WatchProcessesResponse_Snapshot{Snapshot: snapshotMessage(f.Snapshot)}}
	case f.Refused != "":
		return &webv1.WatchProcessesResponse{Event: &webv1.WatchProcessesResponse_Refused{Refused: f.Refused}}
	}
	return &webv1.WatchProcessesResponse{Event: &webv1.WatchProcessesResponse_Offline{Offline: true}}
}

func snapshotMessage(s *liveview.Snapshot) *webv1.ProcessSnapshot {
	sum, tasks := s.Summary, s.Summary.Tasks
	out := &webv1.ProcessSnapshot{
		TakenAt: timestamppb.New(s.TakenAt),
		Summary: &webv1.SystemSummary{
			CpuBusyRatio: sum.CPUBusyRatio, CpuCount: sum.CPUCount,
			MemoryTotalBytes: sum.MemoryTotalBytes, MemoryAvailableBytes: sum.MemoryAvailable,
			SwapTotalBytes: sum.SwapTotalBytes, SwapFreeBytes: sum.SwapFreeBytes,
			Load_1: sum.Load1, Load_5: sum.Load5, Load_15: sum.Load15, UptimeSeconds: sum.UptimeSeconds,
			Tasks: &webv1.TaskCounts{
				Total: tasks.Total, Running: tasks.Running, Sleeping: tasks.Sleeping, Stopped: tasks.Stopped, Zombie: tasks.Zombie,
			},
		},
		Processes: make([]*webv1.ProcessInfo, 0, len(s.Processes)),
		Omitted:   s.Omitted,
	}
	for _, p := range s.Processes {
		out.Processes = append(out.Processes, &webv1.ProcessInfo{
			Pid: p.PID, Ppid: p.PPID, Uid: p.UID, User: p.User, State: p.State,
			CpuRatio: p.CPURatio, RssBytes: p.RSSBytes, MemoryRatio: p.MemoryRatio, Threads: p.Threads,
			RuntimeSeconds: p.RuntimeSeconds, Name: p.Name, Cmdline: p.Cmdline,
			CmdlineTruncated: p.CmdlineTruncated, CmdlineHidden: p.CmdlineHidden, KernelThread: p.KernelThread,
			Unit: p.Unit, Container: p.Container,
		})
	}
	return out
}
