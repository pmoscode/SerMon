package webapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"buf.build/go/protovalidate"
	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/dashboards"
	"github.com/pmoscode/SerMon/server/internal/domain/dashboard"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// DashboardHandler implements sermon.web.v1.DashboardService (M5-23, ADR 0040).
type DashboardHandler struct {
	svc    *dashboards.Service
	logger *slog.Logger
}

var _ webv1connect.DashboardServiceHandler = (*DashboardHandler)(nil)

// NewDashboardHandler returns the DashboardService handler.
func NewDashboardHandler(svc *dashboards.Service, logger *slog.Logger) *DashboardHandler {
	return &DashboardHandler{svc: svc, logger: logger}
}

var (
	widgetKinds = map[webv1.WidgetKind]dashboard.Kind{
		webv1.WidgetKind_WIDGET_KIND_TIMESERIES: dashboard.KindTimeseries, webv1.WidgetKind_WIDGET_KIND_GAUGE: dashboard.KindGauge,
		webv1.WidgetKind_WIDGET_KIND_STAT: dashboard.KindStat, webv1.WidgetKind_WIDGET_KIND_TABLE: dashboard.KindTable,
		webv1.WidgetKind_WIDGET_KIND_LOG_STREAM: dashboard.KindLogStream, webv1.WidgetKind_WIDGET_KIND_ALERT_LIST: dashboard.KindAlertList,
	}
	variableKinds = map[webv1.VariableKind]dashboard.VariableKind{
		webv1.VariableKind_VARIABLE_KIND_NODE: dashboard.VariableNode, webv1.VariableKind_VARIABLE_KIND_GROUP: dashboard.VariableGroup,
	}
	timePresets = map[webv1.TimePreset]dashboard.Preset{
		webv1.TimePreset_TIME_PRESET_UNSPECIFIED: dashboard.PresetUnspecified, webv1.TimePreset_TIME_PRESET_15M: dashboard.Preset15m,
		webv1.TimePreset_TIME_PRESET_1H: dashboard.Preset1h, webv1.TimePreset_TIME_PRESET_6H: dashboard.Preset6h,
		webv1.TimePreset_TIME_PRESET_24H: dashboard.Preset24h, webv1.TimePreset_TIME_PRESET_7D: dashboard.Preset7d,
		webv1.TimePreset_TIME_PRESET_30D: dashboard.Preset30d,
	}
)

// optionalUUID parses an ID protovalidate checked already; empty is the zero UUID.
func optionalUUID(s string) (uuid.UUID, error) {
	if s == "" {
		return uuid.Nil, nil
	}
	return parseID(s)
}

// specOf converts a document; protovalidate checked shapes and bounds before, the domain checks
// the rest.
func specOf(p *webv1.DashboardSpec) (dashboard.Spec, error) {
	s := dashboard.Spec{
		Version: int(p.GetVersion()), Title: p.GetTitle(), Description: p.GetDescription(), TimeRange: timePresets[p.GetTimeRange()],
	}
	for _, v := range p.GetVariables() {
		def, err := optionalUUID(v.GetDefaultId())
		if err != nil {
			return dashboard.Spec{}, err
		}
		s.Variables = append(s.Variables, dashboard.Variable{Name: v.GetName(), Kind: variableKinds[v.GetKind()], Default: def})
	}
	for _, w := range p.GetWidgets() {
		widget, err := widgetOf(w)
		if err != nil {
			return dashboard.Spec{}, err
		}
		s.Widgets = append(s.Widgets, widget)
	}
	return s, nil
}

func widgetOf(w *webv1.Widget) (dashboard.Widget, error) {
	pos, q := w.GetPosition(), w.GetQuery()
	out := dashboard.Widget{
		ID: w.GetId(), Kind: widgetKinds[w.GetKind()], Title: w.GetTitle(),
		Position: dashboard.Position{X: int(pos.GetX()), Y: int(pos.GetY()), W: int(pos.GetW()), H: int(pos.GetH())},
		Query: dashboard.Query{
			Metric: q.GetMetric(), Text: q.GetText(), MinSeverity: int(q.GetMinSeverity()),
			Target: dashboard.Target{Variable: q.GetTarget().GetVariable()},
		},
	}
	if len(q.GetLabels()) > 0 {
		out.Query.Labels = q.GetLabels()
	}
	for _, n := range q.GetTarget().GetNodeIds() {
		id, err := parseID(n)
		if err != nil {
			return dashboard.Widget{}, err
		}
		out.Query.Target.Nodes = append(out.Query.Target.Nodes, identity.NodeID(id))
	}
	group, err := optionalUUID(q.GetTarget().GetGroupId())
	if err != nil {
		return dashboard.Widget{}, err
	}
	if group != uuid.Nil {
		g := tenant.GroupID(group)
		out.Query.Target.Group = &g
	}
	return out, nil
}

func specView(s dashboard.Spec) *webv1.DashboardSpec {
	v := &webv1.DashboardSpec{
		Version: int32(s.Version), Title: s.Title, Description: s.Description, TimeRange: reverse(timePresets)[s.TimeRange], //nolint:gosec // G115: version 1
	}
	for _, variable := range s.Variables {
		def := ""
		if variable.Default != uuid.Nil {
			def = variable.Default.String()
		}
		v.Variables = append(v.Variables, &webv1.DashboardVariable{Name: variable.Name, Kind: reverse(variableKinds)[variable.Kind], DefaultId: def})
	}
	for _, w := range s.Widgets {
		v.Widgets = append(v.Widgets, widgetView(w))
	}
	return v
}

func widgetView(w dashboard.Widget) *webv1.Widget {
	q := w.Query
	target := &webv1.WidgetTarget{Variable: q.Target.Variable}
	for _, n := range q.Target.Nodes {
		target.NodeIds = append(target.NodeIds, n.String())
	}
	if q.Target.Group != nil {
		target.GroupId = q.Target.Group.String()
	}
	return &webv1.Widget{
		Id: w.ID, Kind: reverse(widgetKinds)[w.Kind], Title: w.Title,
		Position: &webv1.WidgetPosition{X: int32(w.Position.X), Y: int32(w.Position.Y), W: int32(w.Position.W), H: int32(w.Position.H)}, //nolint:gosec // G115: cells of the grid
		Query: &webv1.WidgetQuery{
			Metric: q.Metric, Labels: q.Labels, Target: target, Text: q.Text, MinSeverity: webv1.LogSeverity(q.MinSeverity), //nolint:gosec // G115: 0 to 8
		},
	}
}

func dashboardView(d dashboards.Dashboard, spec *webv1.DashboardSpec) *webv1.Dashboard {
	return &webv1.Dashboard{
		Id: d.ID.String(), Spec: spec, OwnerId: d.Owner.String(), OwnerName: d.OwnerName, Shared: d.Shared,
		Revision: d.Revision, CanEdit: d.CanEdit, CreateTime: timestamppb.New(d.CreatedAt), UpdateTime: timestamppb.New(d.UpdatedAt),
	}
}

func dashboardID(s string) (dashboards.ID, error) {
	id, err := parseID(s)
	return dashboards.ID(id), err
}

// ListDashboards lists the caller's and the shared dashboards without their widgets.
func (h *DashboardHandler) ListDashboards(ctx context.Context, _ *connect.Request[webv1.ListDashboardsRequest]) (*connect.Response[webv1.ListDashboardsResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	list, err := h.svc.List(ctx, c)
	if err != nil {
		return nil, dashboardFail(ctx, h.logger, "list dashboards", err)
	}
	res := &webv1.ListDashboardsResponse{}
	for _, d := range list {
		res.Dashboards = append(res.Dashboards, dashboardView(d, &webv1.DashboardSpec{
			Version: int32(d.Spec.Version), Title: d.Spec.Title, Description: d.Spec.Description, //nolint:gosec // G115: version 1
		}))
	}
	return connect.NewResponse(res), nil
}

// GetDashboard returns a dashboard.
func (h *DashboardHandler) GetDashboard(ctx context.Context, req *connect.Request[webv1.GetDashboardRequest]) (*connect.Response[webv1.GetDashboardResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, err := dashboardID(req.Msg.GetDashboardId())
	if err != nil {
		return nil, err
	}
	d, err := h.svc.Get(ctx, c, id)
	if err != nil {
		return nil, dashboardFail(ctx, h.logger, "get dashboard", err)
	}
	return connect.NewResponse(&webv1.GetDashboardResponse{Dashboard: dashboardView(d, specView(d.Spec))}), nil
}

// CreateDashboard adds a dashboard.
func (h *DashboardHandler) CreateDashboard(ctx context.Context, req *connect.Request[webv1.CreateDashboardRequest]) (*connect.Response[webv1.CreateDashboardResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	spec, err := specOf(req.Msg.GetSpec())
	if err != nil {
		return nil, err
	}
	d, err := h.svc.Create(ctx, c, spec)
	if err != nil {
		return nil, dashboardFail(ctx, h.logger, "create dashboard", err)
	}
	return connect.NewResponse(&webv1.CreateDashboardResponse{Dashboard: dashboardView(d, specView(d.Spec))}), nil
}

// UpdateDashboard replaces a document.
func (h *DashboardHandler) UpdateDashboard(ctx context.Context, req *connect.Request[webv1.UpdateDashboardRequest]) (*connect.Response[webv1.UpdateDashboardResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, err := dashboardID(req.Msg.GetDashboardId())
	if err != nil {
		return nil, err
	}
	spec, err := specOf(req.Msg.GetSpec())
	if err != nil {
		return nil, err
	}
	d, err := h.svc.Update(ctx, c, id, req.Msg.GetRevision(), spec)
	if err != nil {
		return nil, dashboardFail(ctx, h.logger, "update dashboard", err)
	}
	return connect.NewResponse(&webv1.UpdateDashboardResponse{Dashboard: dashboardView(d, specView(d.Spec))}), nil
}

// SetDashboardShared shares a dashboard or makes it private.
func (h *DashboardHandler) SetDashboardShared(ctx context.Context, req *connect.Request[webv1.SetDashboardSharedRequest]) (*connect.Response[webv1.SetDashboardSharedResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, err := dashboardID(req.Msg.GetDashboardId())
	if err != nil {
		return nil, err
	}
	d, err := h.svc.SetShared(ctx, c, id, req.Msg.GetShared())
	if err != nil {
		return nil, dashboardFail(ctx, h.logger, "share dashboard", err)
	}
	return connect.NewResponse(&webv1.SetDashboardSharedResponse{Dashboard: dashboardView(d, specView(d.Spec))}), nil
}

// DeleteDashboard removes a dashboard.
func (h *DashboardHandler) DeleteDashboard(ctx context.Context, req *connect.Request[webv1.DeleteDashboardRequest]) (*connect.Response[webv1.DeleteDashboardResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, err := dashboardID(req.Msg.GetDashboardId())
	if err != nil {
		return nil, err
	}
	if err := h.svc.Delete(ctx, c, id); err != nil {
		return nil, dashboardFail(ctx, h.logger, "delete dashboard", err)
	}
	return connect.NewResponse(&webv1.DeleteDashboardResponse{}), nil
}

var errNotADashboard = errors.New("invalid dashboard: not a document of the model")

// ParseDashboardJSON reads a document strictly: unknown fields are rejected, then the rules of
// the schema apply as for a request.
func ParseDashboardJSON(text string) (*webv1.DashboardSpec, error) {
	spec := &webv1.DashboardSpec{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal([]byte(text), spec); err != nil {
		return nil, fmt.Errorf("%w: %w", errNotADashboard, err)
	}
	if err := protovalidate.Validate(spec); err != nil {
		return nil, fmt.Errorf("%w: %w", errNotADashboard, err)
	}
	return spec, nil
}

// ImportDashboard adds a dashboard from JSON.
func (h *DashboardHandler) ImportDashboard(ctx context.Context, req *connect.Request[webv1.ImportDashboardRequest]) (*connect.Response[webv1.ImportDashboardResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	parsed, err := ParseDashboardJSON(req.Msg.GetJson())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	spec, err := specOf(parsed)
	if err != nil {
		return nil, err
	}
	d, err := h.svc.Import(ctx, c, spec)
	if err != nil {
		return nil, dashboardFail(ctx, h.logger, "import dashboard", err)
	}
	return connect.NewResponse(&webv1.ImportDashboardResponse{Dashboard: dashboardView(d, specView(d.Spec))}), nil
}

// ExportDashboard returns the document as JSON.
func (h *DashboardHandler) ExportDashboard(ctx context.Context, req *connect.Request[webv1.ExportDashboardRequest]) (*connect.Response[webv1.ExportDashboardResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, err := dashboardID(req.Msg.GetDashboardId())
	if err != nil {
		return nil, err
	}
	d, err := h.svc.Get(ctx, c, id)
	if err != nil {
		return nil, dashboardFail(ctx, h.logger, "export dashboard", err)
	}
	out, err := (protojson.MarshalOptions{Multiline: true, Indent: "  "}).Marshal(specView(d.Spec))
	if err != nil {
		return nil, internalError(ctx, h.logger, "export dashboard", err)
	}
	return connect.NewResponse(&webv1.ExportDashboardResponse{Json: string(out)}), nil
}

func dashboardFail(ctx context.Context, logger *slog.Logger, op string, err error) error {
	switch {
	case errors.Is(err, access.ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, access.ErrForbidden)
	case errors.Is(err, dashboards.ErrUnknownReference):
		return connect.NewError(connect.CodeNotFound, unwrapTo(err, dashboards.ErrUnknownReference))
	case errors.Is(err, access.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, access.ErrNotFound)
	case errors.Is(err, dashboard.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, unwrapTo(err, dashboard.ErrInvalid))
	case errors.Is(err, dashboards.ErrConflict):
		return connect.NewError(connect.CodeAborted, dashboards.ErrConflict)
	case errors.Is(err, dashboards.ErrTooMany):
		return connect.NewError(connect.CodeResourceExhausted, dashboards.ErrTooMany)
	case errors.Is(err, dashboards.ErrTooManyWrites):
		return connect.NewError(connect.CodeResourceExhausted, dashboards.ErrTooManyWrites)
	}
	return internalError(ctx, logger, op, err)
}

// unwrapTo returns the error that wraps target directly, without the layers above it, so a client
// sees the domain's message ("invalid dashboard: widget …") without "update dashboard: …".
func unwrapTo(err, target error) error {
	for e := err; e != nil; e = errors.Unwrap(e) {
		if errors.Unwrap(e) == target { //nolint:errorlint // the error that wraps target itself
			return e
		}
	}
	return err
}
