package webapi

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/rules"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// AlertRuleHandler implements sermon.web.v1.AlertRuleService (M2-17).
type AlertRuleHandler struct {
	svc    *rules.Service
	logger *slog.Logger
}

var _ webv1connect.AlertRuleServiceHandler = (*AlertRuleHandler)(nil)

// NewAlertRuleHandler returns the AlertRuleService handler.
func NewAlertRuleHandler(svc *rules.Service, logger *slog.Logger) *AlertRuleHandler {
	return &AlertRuleHandler{svc: svc, logger: logger}
}

var origins = map[rules.Origin]webv1.AlertRuleOrigin{
	rules.Default: webv1.AlertRuleOrigin_ALERT_RULE_ORIGIN_DEFAULT,
	rules.Changed: webv1.AlertRuleOrigin_ALERT_RULE_ORIGIN_CHANGED,
	rules.Own:     webv1.AlertRuleOrigin_ALERT_RULE_ORIGIN_OWN,
}

var actions = map[rules.Action]webv1.AlertRuleChangeAction{
	rules.Create:    webv1.AlertRuleChangeAction_ALERT_RULE_CHANGE_ACTION_CREATE,
	rules.Update:    webv1.AlertRuleChangeAction_ALERT_RULE_CHANGE_ACTION_UPDATE,
	rules.Unchanged: webv1.AlertRuleChangeAction_ALERT_RULE_CHANGE_ACTION_UNCHANGED,
}

// ListAlertRules lists the rules.
func (h *AlertRuleHandler) ListAlertRules(ctx context.Context, _ *connect.Request[webv1.ListAlertRulesRequest]) (*connect.Response[webv1.ListAlertRulesResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	list, err := h.svc.List(ctx, c)
	if err != nil {
		return nil, h.fail(ctx, "list alert rules", err)
	}
	res := &webv1.ListAlertRulesResponse{}
	for _, r := range list {
		e := &webv1.AlertRuleEntry{
			Id: string(r.ID), Type: r.Type, Origin: origins[r.Origin], Enabled: r.Enabled, Yaml: r.YAML,
			DefaultYaml: r.DefaultYAML, Problem: r.Problem,
		}
		if r.Doc != nil {
			e.Name, e.Labels, e.Help = r.Doc.GetMetadata().GetName(), r.Doc.GetMetadata().GetLabels(), r.Doc.GetSpec().GetHelp()
		}
		res.Rules = append(res.Rules, e)
	}
	return connect.NewResponse(res), nil
}

// ImportAlertRules checks and stores documents.
func (h *AlertRuleHandler) ImportAlertRules(ctx context.Context, req *connect.Request[webv1.ImportAlertRulesRequest]) (*connect.Response[webv1.ImportAlertRulesResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	result, err := h.svc.Import(ctx, c, []byte(req.Msg.GetYaml()), req.Msg.GetDryRun())
	if err != nil {
		return nil, h.fail(ctx, "import alert rules", err)
	}
	res := &webv1.ImportAlertRulesResponse{Applied: result.Applied}
	for _, ch := range result.Changes {
		res.Changes = append(res.Changes, &webv1.AlertRuleChange{RuleId: string(ch.Rule), Action: actions[ch.Action], BeforeYaml: ch.Before, AfterYaml: ch.After})
	}
	for _, p := range result.Problems {
		res.Problems = append(res.Problems, &webv1.AlertRuleProblem{Document: int32(p.Document), Line: int32(p.Line), Message: p.Message}) //nolint:gosec // G115: bounded by the request size
	}
	return connect.NewResponse(res), nil
}

// SetAlertRuleEnabled switches a rule on or off.
func (h *AlertRuleHandler) SetAlertRuleEnabled(ctx context.Context, req *connect.Request[webv1.SetAlertRuleEnabledRequest]) (*connect.Response[webv1.SetAlertRuleEnabledResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.SetEnabled(ctx, c, alert.RuleID(req.Msg.GetRuleId()), req.Msg.GetEnabled()); err != nil {
		return nil, h.fail(ctx, "switch alert rule", err)
	}
	return connect.NewResponse(&webv1.SetAlertRuleEnabledResponse{}), nil
}

// ResetAlertRule restores the shipped rule.
func (h *AlertRuleHandler) ResetAlertRule(ctx context.Context, req *connect.Request[webv1.ResetAlertRuleRequest]) (*connect.Response[webv1.ResetAlertRuleResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.Reset(ctx, c, alert.RuleID(req.Msg.GetRuleId())); err != nil {
		return nil, h.fail(ctx, "reset alert rule", err)
	}
	return connect.NewResponse(&webv1.ResetAlertRuleResponse{}), nil
}

// ExportAlertRules returns the rules as YAML.
func (h *AlertRuleHandler) ExportAlertRules(ctx context.Context, req *connect.Request[webv1.ExportAlertRulesRequest]) (*connect.Response[webv1.ExportAlertRulesResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	out, err := h.svc.Export(ctx, c, req.Msg.GetAll())
	if err != nil {
		return nil, h.fail(ctx, "export alert rules", err)
	}
	return connect.NewResponse(&webv1.ExportAlertRulesResponse{Yaml: out}), nil
}

func (h *AlertRuleHandler) fail(ctx context.Context, op string, err error) error {
	switch {
	case errors.Is(err, access.ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, access.ErrForbidden)
	case errors.Is(err, access.ErrStepUpRequired):
		return stepUpRequired()
	case errors.Is(err, access.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, access.ErrNotFound)
	}
	return internalError(ctx, h.logger, op, err)
}
