package webapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	"github.com/pmoscode/SerMon/server/internal/app/logs"
	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/metric"
	"github.com/pmoscode/SerMon/server/internal/domain/secevent"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// MetricsHandler implements sermon.web.v1.MetricsService (behind the Authenticator).
type MetricsHandler struct {
	fleet   *fleet.Service
	streams *streamLimiter
	recheck time.Duration
	logger  *slog.Logger
}

var _ webv1connect.MetricsServiceHandler = (*MetricsHandler)(nil)

// NewMetricsHandler returns the MetricsService handler; live are the limits shared with TailLogs
// (nil: its own).
func NewMetricsHandler(svc *fleet.Service, live *LiveStreams, logger *slog.Logger) *MetricsHandler {
	return &MetricsHandler{fleet: svc, streams: live.orNew(), recheck: streamRecheck, logger: logger}
}

// ListMetrics returns the catalog.
func (h *MetricsHandler) ListMetrics(context.Context, *connect.Request[webv1.ListMetricsRequest]) (*connect.Response[webv1.ListMetricsResponse], error) {
	res := &webv1.ListMetricsResponse{}
	for _, d := range metric.All() {
		res.Metrics = append(res.Metrics, &webv1.MetricDefinition{
			Name: d.Name, Unit: string(d.Unit), Labels: d.Labels, Aggregation: string(d.Aggregation), Area: string(d.Area),
			DisplayKey: d.DisplayKey(), HelpKey: d.HelpKey(),
		})
	}
	return connect.NewResponse(res), nil
}

// QueryMetrics returns points of visible nodes.
func (h *MetricsHandler) QueryMetrics(ctx context.Context, req *connect.Request[webv1.QueryMetricsRequest]) (*connect.Response[webv1.QueryMetricsResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	q, err := queryFrom(req.Msg.GetSelector())
	if err != nil {
		return nil, err
	}
	q.From, q.To, q.Step = req.Msg.GetFrom().AsTime(), req.Msg.GetTo().AsTime(), req.Msg.GetStep().AsDuration()
	series, step, err := h.fleet.Query(ctx, c, q)
	if err != nil {
		return nil, readFailure(ctx, h.logger, "query metrics", err)
	}
	return connect.NewResponse(&webv1.QueryMetricsResponse{Series: seriesViews(series), Step: durationpb.New(step)}), nil
}

// WatchMetrics streams new points until the client goes or fleet.WatchDuration is over. Streams
// are limited per session; every streamRecheck the session and rights are checked again: an ended
// session fails the stream, changed rights end it (the client reconnects with the new scope).
func (h *MetricsHandler) WatchMetrics(ctx context.Context, req *connect.Request[webv1.WatchMetricsRequest], stream *connect.ServerStream[webv1.WatchMetricsResponse]) error {
	c, err := callerFrom(ctx)
	if err != nil {
		return err
	}
	q, err := queryFrom(req.Msg.GetSelector())
	if err != nil {
		return err
	}
	s, _ := sessionFrom(ctx)
	key := streamKeyOf(s, c)
	if !h.streams.acquire(key) {
		return connect.NewError(connect.CodeResourceExhausted, errTooManyStreams)
	}
	defer h.streams.release(key)
	recheck := recheckOf(ctx, c)
	checked := time.Now()
	err = h.fleet.Watch(ctx, c, q, func(series []metrics.Series) error {
		if time.Since(checked) >= h.recheck {
			checked = time.Now()
			if err := recheck(); err != nil {
				return err
			}
		}
		return stream.Send(&webv1.WatchMetricsResponse{Series: seriesViews(series)})
	})
	var connectErr *connect.Error
	switch {
	case err == nil, errors.Is(err, context.Canceled), errors.Is(err, errScopeChanged):
		return nil
	case errors.As(err, &connectErr):
		return connectErr
	}
	return readFailure(ctx, h.logger, "watch metrics", err)
}

// FleetHandler implements sermon.web.v1.FleetService (behind the Authenticator).
type FleetHandler struct {
	fleet  *fleet.Service
	logger *slog.Logger
}

var _ webv1connect.FleetServiceHandler = (*FleetHandler)(nil)

// NewFleetHandler returns the FleetService handler.
func NewFleetHandler(svc *fleet.Service, logger *slog.Logger) *FleetHandler {
	return &FleetHandler{fleet: svc, logger: logger}
}

// GetFleetSummary returns the overview.
func (h *FleetHandler) GetFleetSummary(ctx context.Context, req *connect.Request[webv1.GetFleetSummaryRequest]) (*connect.Response[webv1.GetFleetSummaryResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	group, err := optionalGroup(req.Msg.GetGroupId())
	if err != nil {
		return nil, err
	}
	summary, err := h.fleet.Summary(ctx, c, group)
	if err != nil {
		return nil, readFailure(ctx, h.logger, "fleet summary", err)
	}
	res := &webv1.GetFleetSummaryResponse{
		Total: int32(len(summary.Nodes)), Online: int32(summary.Online), Offline: int32(summary.Offline), //nolint:gosec // G115: nodes of one org
	}
	for _, n := range summary.Nodes {
		res.Nodes = append(res.Nodes, &webv1.NodeSummary{
			NodeId: n.ID.String(), Hostname: n.Hostname, GroupId: n.Group.String(), GroupName: n.GroupName,
			OsId: n.OSID, OsVersion: n.OSVersion, Online: n.Online, LastSeenAt: optionalTime(n.LastSeenAt), Figures: figuresView(n.Figures),
			Tags: n.Tags,
		})
	}
	return connect.NewResponse(res), nil
}

// GetNode returns the details of a visible node.
func (h *NodeHandler) GetNode(ctx context.Context, req *connect.Request[webv1.GetNodeRequest]) (*connect.Response[webv1.GetNodeResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.Msg.GetNodeId())
	if err != nil {
		return nil, err
	}
	n, err := h.fleet.Node(ctx, c, identity.NodeID(id))
	if err != nil {
		return nil, readFailure(ctx, h.logger, "get node", err)
	}
	return connect.NewResponse(&webv1.GetNodeResponse{Node: &webv1.Node{
		Id: n.ID.String(), Hostname: n.Hostname, GroupId: n.Group.String(), GroupName: n.GroupName,
		OsId: n.OSID, OsVersion: n.OSVersion, KernelRelease: n.KernelRelease, Architecture: n.Architecture,
		CpuCount: n.CPUCount, MemoryBytes: n.MemoryBytes, AgentVersion: n.AgentVersion,
		EnrolledAt: timestamppb.New(n.EnrolledAt), LastSeenAt: optionalTime(n.LastSeenAt), Online: n.Online, Figures: figuresView(n.Figures),
		LockedAt: optionalTime(n.LockedAt), LockReason: n.LockReason, LastIp: n.LastIP, TakeoverUntil: optionalTime(n.TakeoverUntil),
		Tags: n.Tags, SecuritySource: securitySourceView[n.SecuritySource],
	}}), nil
}

// ListFilesystems returns the file systems of a visible node with the fill forecast.
func (h *NodeHandler) ListFilesystems(ctx context.Context, req *connect.Request[webv1.ListFilesystemsRequest]) (*connect.Response[webv1.ListFilesystemsResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.Msg.GetNodeId())
	if err != nil {
		return nil, err
	}
	filesystems, err := h.fleet.Filesystems(ctx, c, identity.NodeID(id))
	if err != nil {
		return nil, readFailure(ctx, h.logger, "list filesystems", err)
	}
	res := &webv1.ListFilesystemsResponse{}
	for _, fs := range filesystems {
		res.Filesystems = append(res.Filesystems, &webv1.Filesystem{
			Mountpoint: fs.Mountpoint, SizeBytes: fs.Size, UsedBytes: fs.Used, AvailBytes: fs.Avail, UsedRatio: fs.UsedRatio,
			Readonly: fs.Readonly, InodesTotal: fs.InodesTotal, InodesUsed: fs.InodesUsed, FullAt: optionalTime(fs.FullAt),
		})
	}
	return connect.NewResponse(res), nil
}

func queryFrom(selector *webv1.MetricSelector) (metrics.Query, error) {
	q := metrics.Query{Metric: selector.GetMetric(), Labels: selector.GetLabels()}
	for _, raw := range selector.GetNodeIds() {
		id, err := parseID(raw)
		if err != nil {
			return metrics.Query{}, err
		}
		q.Nodes = append(q.Nodes, identity.NodeID(id))
	}
	return q, nil
}

func seriesViews(series []metrics.Series) []*webv1.MetricSeries {
	out := make([]*webv1.MetricSeries, 0, len(series))
	for _, s := range series {
		view := &webv1.MetricSeries{NodeId: s.Node.String(), Labels: s.Labels, Points: make([]*webv1.MetricPoint, len(s.Points))}
		for i, p := range s.Points {
			view.Points[i] = &webv1.MetricPoint{Time: timestamppb.New(p.Time), Avg: p.Avg, Min: p.Min, Max: p.Max}
		}
		out = append(out, view)
	}
	return out
}

func figuresView(f fleet.Figures) *webv1.KeyFigures {
	return &webv1.KeyFigures{
		CpuBusyRatio: f.CPUBusy, MemoryUsedRatio: f.MemoryUsed, FilesystemMaxUsedRatio: f.FilesystemMaxUsed,
		LoadPerCpu: f.LoadPerCPU, UptimeSeconds: f.Uptime,
		NetworkReceiveBytesPerSecond: f.ReceiveBytes, NetworkTransmitBytesPerSecond: f.TransmitBytes,
	}
}

func optionalTime(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}

// readFailure maps read-side errors to Connect codes; unexpected errors are logged and generic.
func readFailure(ctx context.Context, logger *slog.Logger, op string, err error) error {
	switch {
	case errors.Is(err, access.ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, access.ErrForbidden)
	case errors.Is(err, access.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, access.ErrNotFound)
	case errors.Is(err, metrics.ErrInvalidQuery):
		return connect.NewError(connect.CodeInvalidArgument, metrics.ErrInvalidQuery)
	case errors.Is(err, metrics.ErrTooManyPoints):
		return connect.NewError(connect.CodeInvalidArgument, metrics.ErrTooManyPoints)
	}
	return internalError(ctx, logger, op, err)
}

// streamDurations are the maximum durations of the live streams.
var streamDurations = map[string]time.Duration{
	"/sermon.web.v1.MetricsService/WatchMetrics": fleet.WatchDuration,
	"/sermon.web.v1.LogService/TailLogs":         logs.TailDuration,
}

// streamingDeadline lifts the server's write timeout (against slow clients) for the live streams,
// bounded by their own maximum duration.
func streamingDeadline(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if d, ok := streamDurations[r.URL.Path]; ok {
			// An error means the writer can't change deadlines; the stream then ends earlier.
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(d + time.Minute))
		}
		next.ServeHTTP(w, r)
	})
}

// securitySourceView maps the stored source of security events (M2-31); unknown is unspecified.
var securitySourceView = map[secevent.Source]webv1.SecuritySource{
	secevent.SourceJournal:        webv1.SecuritySource_SECURITY_SOURCE_JOURNAL,
	secevent.SourceFileVerified:   webv1.SecuritySource_SECURITY_SOURCE_FILE_VERIFIED,
	secevent.SourceFileUnverified: webv1.SecuritySource_SECURITY_SOURCE_FILE_UNVERIFIED,
	secevent.SourceNone:           webv1.SecuritySource_SECURITY_SOURCE_NONE,
}
