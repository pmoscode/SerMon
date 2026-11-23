package webapi

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/logsettings"
	"github.com/pmoscode/SerMon/server/internal/domain/logconfig"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// LogSettingsHandler implements sermon.web.v1.LogSettingsService (M3-03, ADR 0030).
type LogSettingsHandler struct {
	svc    *logsettings.Service
	logger *slog.Logger
}

var _ webv1connect.LogSettingsServiceHandler = (*LogSettingsHandler)(nil)

// NewLogSettingsHandler returns the handler.
func NewLogSettingsHandler(svc *logsettings.Service, logger *slog.Logger) *LogSettingsHandler {
	return &LogSettingsHandler{svc: svc, logger: logger}
}

// GetLogSettings returns the settings of every group and the rules.
func (h *LogSettingsHandler) GetLogSettings(ctx context.Context, _ *connect.Request[webv1.GetLogSettingsRequest]) (*connect.Response[webv1.GetLogSettingsResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	view, err := h.svc.View(ctx, c)
	if err != nil {
		return nil, h.fail(ctx, "get log settings", err)
	}
	res := &webv1.GetLogSettingsResponse{Version: view.Version, ViewersSeeLogs: view.ViewersSeeLogs}
	for _, n := range view.Nodes {
		res.Nodes = append(res.Nodes, &webv1.NodeConfigStatus{
			NodeId: n.ID.String(), Hostname: n.Hostname, GroupId: n.Group.String(), AppliedVersion: n.AppliedVersion,
			Error: n.Error, LogsVetoed: n.LogsVetoed, ReportedAt: optionalTime(n.ReportedAt),
		})
	}
	for _, g := range view.Groups {
		res.Groups = append(res.Groups, &webv1.GroupLogSettings{
			GroupId: g.ID.String(), GroupName: g.Name, Settings: settingsMessage(g.Settings), Custom: g.Custom,
		})
	}
	for _, r := range view.Rules {
		res.RedactionRules = append(res.RedactionRules, &webv1.RedactionRule{Pattern: r.Pattern})
	}
	return connect.NewResponse(res), nil
}

// SetGroupLogSettings replaces the settings of a group.
func (h *LogSettingsHandler) SetGroupLogSettings(ctx context.Context, req *connect.Request[webv1.SetGroupLogSettingsRequest]) (*connect.Response[webv1.SetGroupLogSettingsResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	group, err := uuid.Parse(req.Msg.GetGroupId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid group id"))
	}
	if err := h.svc.SetGroup(ctx, c, tenant.GroupID(group), settingsFrom(req.Msg.GetSettings())); err != nil {
		return nil, h.fail(ctx, "set log settings", err)
	}
	return connect.NewResponse(&webv1.SetGroupLogSettingsResponse{}), nil
}

// ResetGroupLogSettings returns a group to the defaults.
func (h *LogSettingsHandler) ResetGroupLogSettings(ctx context.Context, req *connect.Request[webv1.ResetGroupLogSettingsRequest]) (*connect.Response[webv1.ResetGroupLogSettingsResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	group, err := uuid.Parse(req.Msg.GetGroupId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid group id"))
	}
	if err := h.svc.ResetGroup(ctx, c, tenant.GroupID(group)); err != nil {
		return nil, h.fail(ctx, "reset log settings", err)
	}
	return connect.NewResponse(&webv1.ResetGroupLogSettingsResponse{}), nil
}

// SetRedactionRules replaces the rules of the organization.
func (h *LogSettingsHandler) SetRedactionRules(ctx context.Context, req *connect.Request[webv1.SetRedactionRulesRequest]) (*connect.Response[webv1.SetRedactionRulesResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	rules := make([]logconfig.Rule, 0, len(req.Msg.GetRules()))
	for _, r := range req.Msg.GetRules() {
		rules = append(rules, logconfig.Rule{Pattern: r.GetPattern()})
	}
	if err := h.svc.SetRules(ctx, c, rules); err != nil {
		return nil, h.fail(ctx, "set redaction rules", err)
	}
	return connect.NewResponse(&webv1.SetRedactionRulesResponse{}), nil
}

// SetViewerLogAccess sets whether viewers may see logs.
func (h *LogSettingsHandler) SetViewerLogAccess(ctx context.Context, req *connect.Request[webv1.SetViewerLogAccessRequest]) (*connect.Response[webv1.SetViewerLogAccessResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.SetViewersSeeLogs(ctx, c, req.Msg.GetViewersSeeLogs()); err != nil {
		return nil, h.fail(ctx, "set viewer log access", err)
	}
	return connect.NewResponse(&webv1.SetViewerLogAccessResponse{}), nil
}

// TestRedaction redacts a sample with rules that are not saved yet.
func (h *LogSettingsHandler) TestRedaction(ctx context.Context, req *connect.Request[webv1.TestRedactionRequest]) (*connect.Response[webv1.TestRedactionResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	rules := make([]logconfig.Rule, 0, len(req.Msg.GetRules()))
	for _, r := range req.Msg.GetRules() {
		rules = append(rules, logconfig.Rule{Pattern: r.GetPattern()})
	}
	out, err := h.svc.TestRedaction(ctx, c, rules, req.Msg.GetSample())
	if err != nil {
		return nil, h.fail(ctx, "test redaction", err)
	}
	return connect.NewResponse(&webv1.TestRedactionResponse{Redacted: out}), nil
}

func (h *LogSettingsHandler) fail(ctx context.Context, op string, err error) error {
	var invalid *logconfig.InvalidError
	switch {
	case errors.Is(err, access.ErrStepUpRequired):
		return stepUpRequired()
	case errors.Is(err, access.ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, access.ErrForbidden)
	case errors.Is(err, access.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, access.ErrNotFound)
	case errors.As(err, &invalid):
		// Only the admin's own input: which field and what the regex parser says about it.
		return connect.NewError(connect.CodeInvalidArgument, invalid)
	}
	return internalError(ctx, h.logger, op, err)
}

func settingsMessage(s logconfig.Settings) *webv1.LogSettings {
	msg := &webv1.LogSettings{
		Enabled: s.Enabled, MinSeverity: webv1.LogSeverity(s.MinSeverity), ExcludeUnits: s.ExcludeUnits, // same codes
	}
	for _, f := range s.Files {
		msg.Files = append(msg.Files, &webv1.LogFileGlob{Pattern: f.Pattern, MultilineStart: f.MultilineStart})
	}
	return msg
}

func settingsFrom(msg *webv1.LogSettings) logconfig.Settings {
	s := logconfig.Settings{
		Enabled:      msg.GetEnabled(),
		MinSeverity:  logconfig.Severity(min(max(msg.GetMinSeverity(), 0), 255)), // bounded; Validate rejects unknown codes
		ExcludeUnits: msg.GetExcludeUnits(),
	}
	for _, f := range msg.GetFiles() {
		s.Files = append(s.Files, logconfig.FileGlob{Pattern: f.GetPattern(), MultilineStart: f.GetMultilineStart()})
	}
	return s
}
