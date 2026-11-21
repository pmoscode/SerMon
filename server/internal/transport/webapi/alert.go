package webapi

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/alerts"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// AlertHandler implements sermon.web.v1.AlertService (M2-16).
type AlertHandler struct {
	svc    *alerts.Service
	logger *slog.Logger
}

var _ webv1connect.AlertServiceHandler = (*AlertHandler)(nil)

// NewAlertHandler returns the AlertService handler.
func NewAlertHandler(svc *alerts.Service, logger *slog.Logger) *AlertHandler {
	return &AlertHandler{svc: svc, logger: logger}
}

// apiState maps the states of the domain (OK = 0) to the API's (OK = 1).
func apiState(s alert.State) webv1.AlertState { return webv1.AlertState(s + 1) } //nolint:gosec // G115: 0..4

func keyOf(k *webv1.AlertKey) (alert.Key, error) {
	id, err := parseID(k.GetNodeId())
	if err != nil {
		return alert.Key{}, err
	}
	return alert.Key{Rule: alert.RuleID(k.GetRuleId()), Node: identity.NodeID(id), Dimension: k.GetDimension()}, nil
}

func keyView(k alert.Key) *webv1.AlertKey {
	return &webv1.AlertKey{RuleId: string(k.Rule), NodeId: k.Node.String(), Dimension: k.Dimension}
}

func alertView(a alerts.Alert) *webv1.Alert {
	v := &webv1.Alert{
		Key: keyView(a.Key), State: apiState(a.State),
		RuleName: &webv1.LocalizedText{De: a.RuleName.De, En: a.RuleName.En}, Labels: a.Labels,
		DimensionLabels: notify.DimensionLabels(a.Key.Dimension), Hostname: a.Hostname,
		GroupId: uuid.UUID(a.Group).String(), GroupName: a.GroupName, Since: timestamppb.New(a.Since),
		FireTime: optionalTimestamp(a.FiredAt), Value: a.Value, Unit: string(a.Unit), Flapping: a.Flapping,
		AcknowledgedBy: a.AckByName, Muted: a.Muted, Fields: a.Fields, CanAcknowledge: a.CanAcknowledge,
	}
	if a.Ack != nil {
		v.AcknowledgeTime = timestamppb.New(a.Ack.At)
	}
	return v
}

// ListAlerts lists the active alerts.
func (h *AlertHandler) ListAlerts(ctx context.Context, _ *connect.Request[webv1.ListAlertsRequest]) (*connect.Response[webv1.ListAlertsResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	list, err := h.svc.List(ctx, c)
	if err != nil {
		return nil, h.fail(ctx, "list alerts", err)
	}
	res := &webv1.ListAlertsResponse{}
	for _, a := range list {
		res.Alerts = append(res.Alerts, alertView(a))
	}
	return connect.NewResponse(res), nil
}

// GetAlertHistory returns the history of an instance.
func (h *AlertHandler) GetAlertHistory(ctx context.Context, req *connect.Request[webv1.GetAlertHistoryRequest]) (*connect.Response[webv1.GetAlertHistoryResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	key, err := keyOf(req.Msg.GetKey())
	if err != nil {
		return nil, err
	}
	entries, err := h.svc.History(ctx, c, key)
	if err != nil {
		return nil, h.fail(ctx, "alert history", err)
	}
	res := &webv1.GetAlertHistoryResponse{}
	for _, e := range entries {
		t := e.Transition
		res.Entries = append(res.Entries, &webv1.AlertHistoryEntry{
			Kind: webv1.AlertHistoryKind(t.Kind), From: apiState(t.From), To: apiState(t.To), //nolint:gosec // G115: 1..4
			Time: timestamppb.New(t.At), Value: t.Value, By: e.ByName, Fields: t.Fields,
		})
	}
	return connect.NewResponse(res), nil
}

// AcknowledgeAlert acknowledges a firing alert.
func (h *AlertHandler) AcknowledgeAlert(ctx context.Context, req *connect.Request[webv1.AcknowledgeAlertRequest]) (*connect.Response[webv1.AcknowledgeAlertResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	key, err := keyOf(req.Msg.GetKey())
	if err != nil {
		return nil, err
	}
	if err := h.svc.Acknowledge(ctx, c, key); err != nil {
		return nil, h.fail(ctx, "acknowledge alert", err)
	}
	return connect.NewResponse(&webv1.AcknowledgeAlertResponse{}), nil
}

func (h *AlertHandler) fail(ctx context.Context, op string, err error) error {
	switch {
	case errors.Is(err, access.ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, access.ErrForbidden)
	case errors.Is(err, access.ErrStepUpRequired):
		return stepUpRequired()
	case errors.Is(err, access.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, access.ErrNotFound)
	case errors.Is(err, alerts.ErrNotFiring):
		return connect.NewError(connect.CodeFailedPrecondition, alerts.ErrNotFiring)
	}
	return internalError(ctx, h.logger, op, err)
}
