package webapi

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/logs"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/logql"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// LogHandler implements sermon.web.v1.LogService (M3-08).
type LogHandler struct {
	svc    *logs.Service
	logger *slog.Logger
}

var _ webv1connect.LogServiceHandler = (*LogHandler)(nil)

// NewLogHandler returns the handler.
func NewLogHandler(svc *logs.Service, logger *slog.Logger) *LogHandler {
	return &LogHandler{svc: svc, logger: logger}
}

// QueryLogs returns the lines of the filter.
func (h *LogHandler) QueryLogs(ctx context.Context, req *connect.Request[webv1.QueryLogsRequest]) (*connect.Response[webv1.QueryLogsResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	f, err := logFilterFrom(req.Msg.GetFilter())
	if err != nil {
		return nil, err
	}
	page, err := h.svc.Query(ctx, c, f, int(req.Msg.GetLimit()), req.Msg.GetOldestFirst())
	if err != nil {
		return nil, h.fail(ctx, "query logs", err)
	}
	res := &webv1.QueryLogsResponse{More: page.More, Lines: make([]*webv1.LogLine, 0, len(page.Lines))}
	for _, l := range page.Lines {
		res.Lines = append(res.Lines, logLineMessage(l))
	}
	return connect.NewResponse(res), nil
}

// GetLogHistogram counts the lines per severity.
func (h *LogHandler) GetLogHistogram(ctx context.Context, req *connect.Request[webv1.GetLogHistogramRequest]) (*connect.Response[webv1.GetLogHistogramResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	f, err := logFilterFrom(req.Msg.GetFilter())
	if err != nil {
		return nil, err
	}
	hist, err := h.svc.Histogram(ctx, c, f)
	if err != nil {
		return nil, h.fail(ctx, "log histogram", err)
	}
	res := &webv1.GetLogHistogramResponse{Step: durationpb.New(hist.Step)}
	for _, severity := range logql.Severities {
		points := hist.Series[severity]
		if len(points) == 0 {
			continue
		}
		series := &webv1.LogHistogramSeries{Severity: severityMessage(severity)}
		for _, p := range points {
			series.Points = append(series.Points, &webv1.LogHistogramPoint{At: timestamppb.New(p.At), Count: p.Value})
		}
		res.Series = append(res.Series, series)
	}
	return connect.NewResponse(res), nil
}

// GetLogContext returns the lines around an entry.
func (h *LogHandler) GetLogContext(ctx context.Context, req *connect.Request[webv1.GetLogContextRequest]) (*connect.Response[webv1.GetLogContextResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, err := uuid.Parse(req.Msg.GetNodeId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errInvalidID)
	}
	lines, err := h.svc.Context(ctx, c, identity.NodeID(id), req.Msg.GetAt().AsTime(), sourceFrom(req.Msg.GetSource()), req.Msg.GetUnit())
	if err != nil {
		return nil, h.fail(ctx, "log context", err)
	}
	res := &webv1.GetLogContextResponse{Lines: make([]*webv1.LogLine, 0, len(lines))}
	for _, l := range lines {
		res.Lines = append(res.Lines, logLineMessage(l))
	}
	return connect.NewResponse(res), nil
}

func (h *LogHandler) fail(ctx context.Context, op string, err error) error {
	switch {
	case errors.Is(err, access.ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, access.ErrForbidden)
	case errors.Is(err, access.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, access.ErrNotFound)
	case errors.Is(err, logs.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, logs.ErrUnavailable):
		h.logger.WarnContext(ctx, op+": log store unavailable", "error", err)
		return connect.NewError(connect.CodeUnavailable, logs.ErrUnavailable)
	}
	return internalError(ctx, h.logger, op, err)
}

func logFilterFrom(m *webv1.LogFilter) (logs.Filter, error) {
	f := logs.Filter{
		From: m.GetFrom().AsTime(), To: m.GetTo().AsTime(), Units: m.GetUnits(),
		Text: m.GetText(), IgnoreCase: m.GetIgnoreCase(),
	}
	for _, raw := range m.GetNodeIds() {
		id, err := uuid.Parse(raw)
		if err != nil {
			return logs.Filter{}, connect.NewError(connect.CodeInvalidArgument, errInvalidID)
		}
		f.Nodes = append(f.Nodes, identity.NodeID(id))
	}
	if m.GroupId != nil {
		id, err := uuid.Parse(m.GetGroupId())
		if err != nil {
			return logs.Filter{}, connect.NewError(connect.CodeInvalidArgument, errInvalidID)
		}
		group := tenant.GroupID(id)
		f.Group = &group
	}
	for _, s := range m.GetSources() {
		f.Sources = append(f.Sources, sourceFrom(s))
	}
	if code := int(m.GetMinSeverity()); code > 0 && code <= len(logql.Severities)-1 {
		f.MinSeverity = logql.Severities[code-1]
	}
	return f, nil
}

func sourceFrom(s webv1.LogSource) logql.Source {
	if s == webv1.LogSource_LOG_SOURCE_FILE {
		return logql.SourceFile
	}
	return logql.SourceJournal
}

// severityMessage: emergency … debug are 1 … 8 as in logql.Severities; unknown is unspecified.
func severityMessage(s logql.Severity) webv1.LogSeverity {
	for i, known := range logql.Severities {
		if known == s && s != logql.SeverityUnknown {
			return webv1.LogSeverity(i + 1) // at most 8
		}
	}
	return webv1.LogSeverity_LOG_SEVERITY_UNSPECIFIED
}

func logLineMessage(l logs.Line) *webv1.LogLine {
	source := webv1.LogSource_LOG_SOURCE_UNSPECIFIED
	switch l.Source {
	case logql.SourceJournal:
		source = webv1.LogSource_LOG_SOURCE_JOURNAL
	case logql.SourceFile:
		source = webv1.LogSource_LOG_SOURCE_FILE
	}
	return &webv1.LogLine{
		NodeId: l.Node.String(), Hostname: l.Hostname, At: timestamppb.New(l.At), Severity: severityMessage(l.Severity),
		Source: source, Unit: l.Unit, Message: l.Message, Truncated: l.Truncated, Fields: l.Fields,
	}
}
