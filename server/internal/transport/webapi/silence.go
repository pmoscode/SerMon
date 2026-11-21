package webapi

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/routing"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// AlertSilenceHandler implements sermon.web.v1.AlertSilenceService (M2-15).
type AlertSilenceHandler struct {
	svc    *routing.SilenceService
	logger *slog.Logger
}

var _ webv1connect.AlertSilenceServiceHandler = (*AlertSilenceHandler)(nil)

// NewAlertSilenceHandler returns the AlertSilenceService handler.
func NewAlertSilenceHandler(svc *routing.SilenceService, logger *slog.Logger) *AlertSilenceHandler {
	return &AlertSilenceHandler{svc: svc, logger: logger}
}

// matchersOf converts matchers; protovalidate checked shapes and bounds before.
func matchersOf(m *webv1.PolicyMatchers) (routing.Matchers, error) {
	out := routing.Matchers{Rules: m.GetRules(), Labels: m.GetLabels()}
	for _, s := range m.GetSeverities() {
		out.Severities = append(out.Severities, severities[s])
	}
	for _, g := range m.GetGroupIds() {
		id, err := parseID(g)
		if err != nil {
			return routing.Matchers{}, err
		}
		out.Groups = append(out.Groups, tenant.GroupID(id))
	}
	return out, nil
}

func matchersView(m routing.Matchers) *webv1.PolicyMatchers {
	severityOf := reverse(severities)
	v := &webv1.PolicyMatchers{Rules: m.Rules, Labels: m.Labels}
	for _, s := range m.Severities {
		v.Severities = append(v.Severities, severityOf[s])
	}
	for _, g := range m.Groups {
		v.GroupIds = append(v.GroupIds, uuid.UUID(g).String())
	}
	return v
}

func silenceOf(spec *webv1.SilenceSpec) (routing.Silence, error) {
	m, err := matchersOf(spec.GetMatchers())
	if err != nil {
		return routing.Silence{}, err
	}
	s := routing.Silence{Match: m, EndsAt: spec.GetEndTime().AsTime(), Comment: spec.GetComment()}
	if spec.GetStartTime() != nil {
		s.StartsAt = spec.GetStartTime().AsTime()
	}
	for _, n := range spec.GetNodeIds() {
		id, err := parseID(n)
		if err != nil {
			return routing.Silence{}, err
		}
		s.Nodes = append(s.Nodes, identity.NodeID(id))
	}
	return s, nil
}

func userID(u identity.UserID) string {
	if u.IsZero() {
		return ""
	}
	return u.String()
}

func silenceView(s routing.Silence) *webv1.AlertSilence {
	spec := &webv1.SilenceSpec{
		Matchers: matchersView(s.Match), StartTime: timestamppb.New(s.StartsAt), EndTime: timestamppb.New(s.EndsAt), Comment: s.Comment,
	}
	for _, n := range s.Nodes {
		spec.NodeIds = append(spec.NodeIds, n.String())
	}
	return &webv1.AlertSilence{
		Id: s.ID.String(), Spec: spec, CreatedBy: userID(s.CreatedBy), ExpiredBy: userID(s.ExpiredBy), CreateTime: timestamppb.New(s.CreatedAt),
	}
}

// ListSilences lists the silences the caller may see.
func (h *AlertSilenceHandler) ListSilences(ctx context.Context, _ *connect.Request[webv1.ListSilencesRequest]) (*connect.Response[webv1.ListSilencesResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	list, err := h.svc.List(ctx, c)
	if err != nil {
		return nil, routingFail(ctx, h.logger, "list silences", err)
	}
	res := &webv1.ListSilencesResponse{}
	for _, s := range list {
		res.Silences = append(res.Silences, silenceView(s))
	}
	return connect.NewResponse(res), nil
}

// CreateSilence adds a silence.
func (h *AlertSilenceHandler) CreateSilence(ctx context.Context, req *connect.Request[webv1.CreateSilenceRequest]) (*connect.Response[webv1.CreateSilenceResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	s, err := silenceOf(req.Msg.GetSpec())
	if err != nil {
		return nil, err
	}
	created, err := h.svc.Create(ctx, c, s)
	if err != nil {
		return nil, routingFail(ctx, h.logger, "create silence", err)
	}
	return connect.NewResponse(&webv1.CreateSilenceResponse{Silence: silenceView(created)}), nil
}

// ExpireSilence ends a silence.
func (h *AlertSilenceHandler) ExpireSilence(ctx context.Context, req *connect.Request[webv1.ExpireSilenceRequest]) (*connect.Response[webv1.ExpireSilenceResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.Msg.GetSilenceId())
	if err != nil {
		return nil, err
	}
	if err := h.svc.Expire(ctx, c, routing.SilenceID(id)); err != nil {
		return nil, routingFail(ctx, h.logger, "expire silence", err)
	}
	return connect.NewResponse(&webv1.ExpireSilenceResponse{}), nil
}

// MaintenanceWindowHandler implements sermon.web.v1.MaintenanceWindowService (M2-15).
type MaintenanceWindowHandler struct {
	svc    *routing.Service
	logger *slog.Logger
}

var _ webv1connect.MaintenanceWindowServiceHandler = (*MaintenanceWindowHandler)(nil)

// NewMaintenanceWindowHandler returns the MaintenanceWindowService handler.
func NewMaintenanceWindowHandler(svc *routing.Service, logger *slog.Logger) *MaintenanceWindowHandler {
	return &MaintenanceWindowHandler{svc: svc, logger: logger}
}

// weekday maps the API's Monday-first days to Go's (Sunday is 0).
func weekday(d webv1.Weekday) time.Weekday {
	if d == webv1.Weekday_WEEKDAY_SUNDAY {
		return time.Sunday
	}
	return time.Weekday(d)
}

func apiWeekday(d time.Weekday) webv1.Weekday {
	if d == time.Sunday {
		return webv1.Weekday_WEEKDAY_SUNDAY
	}
	return webv1.Weekday(d) //nolint:gosec // G115: a weekday
}

func windowOf(spec *webv1.MaintenanceWindowSpec) (routing.MaintenanceWindow, error) {
	w := routing.MaintenanceWindow{
		Name: spec.GetName(), Enabled: spec.GetEnabled(),
		Schedule: routing.Schedule{
			Start: time.Duration(spec.GetStartMinute()) * time.Minute, Duration: time.Duration(spec.GetDurationMinutes()) * time.Minute,
			TimeZone: spec.GetTimeZone(),
		},
	}
	for _, d := range spec.GetDays() {
		w.Schedule.Days = append(w.Schedule.Days, weekday(d))
	}
	for _, g := range spec.GetGroupIds() {
		id, err := parseID(g)
		if err != nil {
			return routing.MaintenanceWindow{}, err
		}
		w.Groups = append(w.Groups, tenant.GroupID(id))
	}
	return w, nil
}

func windowView(w routing.MaintenanceWindow) *webv1.MaintenanceWindow {
	spec := &webv1.MaintenanceWindowSpec{
		Name: w.Name, Enabled: w.Enabled, TimeZone: w.Schedule.TimeZone,
		StartMinute: int32(w.Schedule.Start / time.Minute), DurationMinutes: int32(w.Schedule.Duration / time.Minute), //nolint:gosec // G115: at most a day
	}
	for _, d := range w.Schedule.Days {
		spec.Days = append(spec.Days, apiWeekday(d))
	}
	for _, g := range w.Groups {
		spec.GroupIds = append(spec.GroupIds, uuid.UUID(g).String())
	}
	return &webv1.MaintenanceWindow{Id: w.ID.String(), Spec: spec, CreateTime: timestamppb.New(w.CreatedAt), UpdateTime: timestamppb.New(w.UpdatedAt)}
}

// ListMaintenanceWindows lists the windows.
func (h *MaintenanceWindowHandler) ListMaintenanceWindows(ctx context.Context, _ *connect.Request[webv1.ListMaintenanceWindowsRequest]) (*connect.Response[webv1.ListMaintenanceWindowsResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	list, err := h.svc.ListWindows(ctx, c)
	if err != nil {
		return nil, routingFail(ctx, h.logger, "list maintenance windows", err)
	}
	res := &webv1.ListMaintenanceWindowsResponse{}
	for _, w := range list {
		res.Windows = append(res.Windows, windowView(w))
	}
	return connect.NewResponse(res), nil
}

// CreateMaintenanceWindow adds a window.
func (h *MaintenanceWindowHandler) CreateMaintenanceWindow(ctx context.Context, req *connect.Request[webv1.CreateMaintenanceWindowRequest]) (*connect.Response[webv1.CreateMaintenanceWindowResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	w, err := windowOf(req.Msg.GetSpec())
	if err != nil {
		return nil, err
	}
	created, err := h.svc.CreateWindow(ctx, c, w)
	if err != nil {
		return nil, routingFail(ctx, h.logger, "create maintenance window", err)
	}
	return connect.NewResponse(&webv1.CreateMaintenanceWindowResponse{Window: windowView(created)}), nil
}

// UpdateMaintenanceWindow replaces a window.
func (h *MaintenanceWindowHandler) UpdateMaintenanceWindow(ctx context.Context, req *connect.Request[webv1.UpdateMaintenanceWindowRequest]) (*connect.Response[webv1.UpdateMaintenanceWindowResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.Msg.GetWindowId())
	if err != nil {
		return nil, err
	}
	w, err := windowOf(req.Msg.GetSpec())
	if err != nil {
		return nil, err
	}
	updated, err := h.svc.UpdateWindow(ctx, c, routing.WindowID(id), w)
	if err != nil {
		return nil, routingFail(ctx, h.logger, "update maintenance window", err)
	}
	return connect.NewResponse(&webv1.UpdateMaintenanceWindowResponse{Window: windowView(updated)}), nil
}

// DeleteMaintenanceWindow removes a window.
func (h *MaintenanceWindowHandler) DeleteMaintenanceWindow(ctx context.Context, req *connect.Request[webv1.DeleteMaintenanceWindowRequest]) (*connect.Response[webv1.DeleteMaintenanceWindowResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.Msg.GetWindowId())
	if err != nil {
		return nil, err
	}
	if err := h.svc.DeleteWindow(ctx, c, routing.WindowID(id)); err != nil {
		return nil, routingFail(ctx, h.logger, "delete maintenance window", err)
	}
	return connect.NewResponse(&webv1.DeleteMaintenanceWindowResponse{}), nil
}

// routingFail maps the errors of the routing use cases; their messages name fields, never values.
func routingFail(ctx context.Context, logger *slog.Logger, op string, err error) error {
	switch {
	case errors.Is(err, access.ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, access.ErrForbidden)
	case errors.Is(err, access.ErrStepUpRequired):
		return stepUpRequired()
	case errors.Is(err, access.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, access.ErrNotFound)
	case errors.Is(err, routing.ErrInvalidPolicy), errors.Is(err, routing.ErrInvalidSilence), errors.Is(err, routing.ErrInvalidWindow):
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return internalError(ctx, logger, op, err)
}
