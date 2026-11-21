package webapi

import (
	"context"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/app/routing"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// NotificationPolicyHandler implements sermon.web.v1.NotificationPolicyService (M2-14).
type NotificationPolicyHandler struct {
	svc    *routing.Service
	logger *slog.Logger
}

var _ webv1connect.NotificationPolicyServiceHandler = (*NotificationPolicyHandler)(nil)

// NewNotificationPolicyHandler returns the NotificationPolicyService handler.
func NewNotificationPolicyHandler(svc *routing.Service, logger *slog.Logger) *NotificationPolicyHandler {
	return &NotificationPolicyHandler{svc: svc, logger: logger}
}

var (
	severities = map[webv1.AlertSeverity]notify.Severity{
		webv1.AlertSeverity_ALERT_SEVERITY_WARNING: notify.Warning, webv1.AlertSeverity_ALERT_SEVERITY_CRITICAL: notify.Critical,
	}
	groupings = map[webv1.PolicyGrouping]routing.Grouping{
		webv1.PolicyGrouping_POLICY_GROUPING_NODE: routing.GroupByNode, webv1.PolicyGrouping_POLICY_GROUPING_DIGEST: routing.GroupDigest,
	}
)

func reverse[K, V comparable](m map[K]V) map[V]K {
	out := make(map[V]K, len(m))
	for k, v := range m {
		out[v] = k
	}
	return out
}

func channelIDs(ids []string) ([]notify.ChannelID, error) {
	out := make([]notify.ChannelID, 0, len(ids))
	for _, s := range ids {
		id, err := parseID(s)
		if err != nil {
			return nil, err
		}
		out = append(out, notify.ChannelID(id))
	}
	return out, nil
}

// policyOf converts a spec; protovalidate checked shapes and bounds before.
func policyOf(spec *webv1.PolicySpec) (routing.Policy, error) {
	p := routing.Policy{
		Name: spec.GetName(), Enabled: spec.GetEnabled(), Grouping: groupings[spec.GetGrouping()],
		Repeat: time.Duration(spec.GetRepeatMinutes()) * time.Minute,
	}
	var err error
	if p.Channels, err = channelIDs(spec.GetChannelIds()); err != nil {
		return routing.Policy{}, err
	}
	if e := spec.GetEscalation(); e != nil {
		p.Escalation = &routing.Escalation{After: time.Duration(e.GetAfterMinutes()) * time.Minute}
		if p.Escalation.Channels, err = channelIDs(e.GetChannelIds()); err != nil {
			return routing.Policy{}, err
		}
	}
	if p.Match, err = matchersOf(spec.GetMatchers()); err != nil {
		return routing.Policy{}, err
	}
	return p, nil
}

func policyView(p routing.Policy) *webv1.NotificationPolicy {
	groupingOf := reverse(groupings)
	spec := &webv1.PolicySpec{
		Name: p.Name, Enabled: p.Enabled, Grouping: groupingOf[p.Grouping], RepeatMinutes: int32(p.Repeat / time.Minute), //nolint:gosec // G115: at most a week
		Matchers: matchersView(p.Match),
	}
	for _, c := range p.Channels {
		spec.ChannelIds = append(spec.ChannelIds, c.String())
	}
	if e := p.Escalation; e != nil {
		spec.Escalation = &webv1.PolicyEscalation{AfterMinutes: int32(e.After / time.Minute)} //nolint:gosec // G115: at most a day
		for _, c := range e.Channels {
			spec.Escalation.ChannelIds = append(spec.Escalation.ChannelIds, c.String())
		}
	}
	return &webv1.NotificationPolicy{Id: p.ID.String(), Spec: spec, CreateTime: timestamppb.New(p.CreatedAt), UpdateTime: timestamppb.New(p.UpdatedAt)}
}

// ListPolicies lists the policies.
func (h *NotificationPolicyHandler) ListPolicies(ctx context.Context, _ *connect.Request[webv1.ListPoliciesRequest]) (*connect.Response[webv1.ListPoliciesResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	list, err := h.svc.List(ctx, c)
	if err != nil {
		return nil, h.fail(ctx, "list policies", err)
	}
	res := &webv1.ListPoliciesResponse{}
	for _, p := range list {
		res.Policies = append(res.Policies, policyView(p))
	}
	return connect.NewResponse(res), nil
}

// CreatePolicy adds a policy.
func (h *NotificationPolicyHandler) CreatePolicy(ctx context.Context, req *connect.Request[webv1.CreatePolicyRequest]) (*connect.Response[webv1.CreatePolicyResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	p, err := policyOf(req.Msg.GetSpec())
	if err != nil {
		return nil, err
	}
	created, err := h.svc.Create(ctx, c, p)
	if err != nil {
		return nil, h.fail(ctx, "create policy", err)
	}
	return connect.NewResponse(&webv1.CreatePolicyResponse{Policy: policyView(created)}), nil
}

// UpdatePolicy replaces a policy.
func (h *NotificationPolicyHandler) UpdatePolicy(ctx context.Context, req *connect.Request[webv1.UpdatePolicyRequest]) (*connect.Response[webv1.UpdatePolicyResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.Msg.GetPolicyId())
	if err != nil {
		return nil, err
	}
	p, err := policyOf(req.Msg.GetSpec())
	if err != nil {
		return nil, err
	}
	updated, err := h.svc.Update(ctx, c, routing.PolicyID(id), p)
	if err != nil {
		return nil, h.fail(ctx, "update policy", err)
	}
	return connect.NewResponse(&webv1.UpdatePolicyResponse{Policy: policyView(updated)}), nil
}

// DeletePolicy removes a policy.
func (h *NotificationPolicyHandler) DeletePolicy(ctx context.Context, req *connect.Request[webv1.DeletePolicyRequest]) (*connect.Response[webv1.DeletePolicyResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.Msg.GetPolicyId())
	if err != nil {
		return nil, err
	}
	if err := h.svc.Delete(ctx, c, routing.PolicyID(id)); err != nil {
		return nil, h.fail(ctx, "delete policy", err)
	}
	return connect.NewResponse(&webv1.DeletePolicyResponse{}), nil
}

func (h *NotificationPolicyHandler) fail(ctx context.Context, op string, err error) error {
	return routingFail(ctx, h.logger, op, err)
}
