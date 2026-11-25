package webapi

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"connectrpc.com/connect"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/routing"
	"github.com/pmoscode/SerMon/server/internal/app/updates"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// UpdatePolicyHandler implements sermon.web.v1.UpdatePolicyService (behind the Authenticator,
// M4-10).
type UpdatePolicyHandler struct {
	policies *updates.PolicyService
	logger   *slog.Logger
}

var _ webv1connect.UpdatePolicyServiceHandler = (*UpdatePolicyHandler)(nil)

// NewUpdatePolicyHandler returns the UpdatePolicyService handler.
func NewUpdatePolicyHandler(svc *updates.PolicyService, logger *slog.Logger) *UpdatePolicyHandler {
	return &UpdatePolicyHandler{policies: svc, logger: logger}
}

func (h *UpdatePolicyHandler) fail(ctx context.Context, op string, err error) error {
	switch {
	case errors.Is(err, access.ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, access.ErrForbidden)
	case errors.Is(err, access.ErrStepUpRequired):
		return stepUpRequired()
	case errors.Is(err, access.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, access.ErrNotFound)
	case errors.Is(err, updates.ErrInvalidPolicy):
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return internalError(ctx, h.logger, op, err)
}

func groupOf(raw string) (tenant.GroupID, error) {
	id, err := parseID(raw)
	return tenant.GroupID(id), err
}

var apiModes = map[updates.Mode]webv1.UpdateMode{
	updates.ModeReport:       webv1.UpdateMode_UPDATE_MODE_REPORT,
	updates.ModeSecurityAuto: webv1.UpdateMode_UPDATE_MODE_SECURITY_AUTO,
	updates.ModeAllAuto:      webv1.UpdateMode_UPDATE_MODE_ALL_AUTO,
}

func updatePolicyOf(group tenant.GroupID, spec *webv1.UpdatePolicySpec) updates.Policy {
	p := updates.Policy{
		Group: group, AutoReboot: spec.GetAutoReboot(),
		Rollout: updates.Rollout{
			MaxParallel: int(spec.GetMaxParallel()), StopOnFailure: spec.GetStopOnFailure(),
			HealthCheck: time.Duration(spec.GetHealthCheckMinutes()) * time.Minute,
		},
	}
	for m, v := range apiModes {
		if v == spec.GetMode() {
			p.Mode = m
		}
	}
	if w := spec.GetWindow(); w != nil {
		s := &routing.Schedule{
			Start: time.Duration(w.GetStartMinute()) * time.Minute, Duration: time.Duration(w.GetDurationMinutes()) * time.Minute,
			TimeZone: w.GetTimeZone(),
		}
		for _, d := range w.GetDays() {
			s.Days = append(s.Days, weekday(d))
		}
		p.Window = s
	}
	return p
}

func updatePolicyView(p updates.Policy, name string) *webv1.UpdatePolicy {
	spec := &webv1.UpdatePolicySpec{
		Mode: apiModes[p.Mode], AutoReboot: p.AutoReboot, MaxParallel: int32(p.Rollout.MaxParallel), //nolint:gosec // 1 to 50
		StopOnFailure: p.Rollout.StopOnFailure, HealthCheckMinutes: int32(p.Rollout.HealthCheck / time.Minute), //nolint:gosec // 5 to 120
	}
	if w := p.Window; w != nil {
		spec.Window = &webv1.UpdateWindow{
			StartMinute: int32(w.Start / time.Minute), DurationMinutes: int32(w.Duration / time.Minute), //nolint:gosec // within a day
			TimeZone: w.TimeZone,
		}
		for _, d := range w.Days {
			spec.Window.Days = append(spec.Window.Days, apiWeekday(d))
		}
	}
	return &webv1.UpdatePolicy{
		GroupId: p.Group.String(), GroupName: name, Spec: spec, IsDefault: p.UpdatedAt.IsZero(),
		UpdateTime: optionalTimestamp(p.UpdatedAt),
	}
}

// GetUpdatePolicy returns a visible group's policy.
func (h *UpdatePolicyHandler) GetUpdatePolicy(ctx context.Context, req *connect.Request[webv1.GetUpdatePolicyRequest]) (*connect.Response[webv1.GetUpdatePolicyResponse], error) {
	c, group, err := h.callerAndGroup(ctx, req.Msg.GetGroupId())
	if err != nil {
		return nil, err
	}
	p, err := h.policies.Get(ctx, c, group)
	if err != nil {
		return nil, h.fail(ctx, "get update policy", err)
	}
	return connect.NewResponse(&webv1.GetUpdatePolicyResponse{Policy: updatePolicyView(p, "")}), nil
}

// ListUpdatePolicies returns the policies of the visible groups.
func (h *UpdatePolicyHandler) ListUpdatePolicies(ctx context.Context, _ *connect.Request[webv1.ListUpdatePoliciesRequest]) (*connect.Response[webv1.ListUpdatePoliciesResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	list, err := h.policies.List(ctx, c)
	if err != nil {
		return nil, h.fail(ctx, "list update policies", err)
	}
	res := &webv1.ListUpdatePoliciesResponse{Policies: make([]*webv1.UpdatePolicy, 0, len(list))}
	for _, n := range list {
		res.Policies = append(res.Policies, updatePolicyView(n.Policy, n.GroupName))
	}
	return connect.NewResponse(res), nil
}

// SetUpdatePolicy replaces a group's policy.
func (h *UpdatePolicyHandler) SetUpdatePolicy(ctx context.Context, req *connect.Request[webv1.SetUpdatePolicyRequest]) (*connect.Response[webv1.SetUpdatePolicyResponse], error) {
	c, group, err := h.callerAndGroup(ctx, req.Msg.GetGroupId())
	if err != nil {
		return nil, err
	}
	p, err := h.policies.Set(ctx, c, updatePolicyOf(group, req.Msg.GetSpec()))
	if err != nil {
		return nil, h.fail(ctx, "set update policy", err)
	}
	return connect.NewResponse(&webv1.SetUpdatePolicyResponse{Policy: updatePolicyView(p, "")}), nil
}

// ImportUpdatePolicy sets a group's policy from YAML.
func (h *UpdatePolicyHandler) ImportUpdatePolicy(ctx context.Context, req *connect.Request[webv1.ImportUpdatePolicyRequest]) (*connect.Response[webv1.ImportUpdatePolicyResponse], error) {
	c, group, err := h.callerAndGroup(ctx, req.Msg.GetGroupId())
	if err != nil {
		return nil, err
	}
	p, err := h.policies.Import(ctx, c, group, []byte(req.Msg.GetYaml()))
	if err != nil {
		return nil, h.fail(ctx, "import update policy", err)
	}
	return connect.NewResponse(&webv1.ImportUpdatePolicyResponse{Policy: updatePolicyView(p, "")}), nil
}

// ExportUpdatePolicy returns a visible group's policy as YAML.
func (h *UpdatePolicyHandler) ExportUpdatePolicy(ctx context.Context, req *connect.Request[webv1.ExportUpdatePolicyRequest]) (*connect.Response[webv1.ExportUpdatePolicyResponse], error) {
	c, group, err := h.callerAndGroup(ctx, req.Msg.GetGroupId())
	if err != nil {
		return nil, err
	}
	p, err := h.policies.Get(ctx, c, group)
	if err != nil {
		return nil, h.fail(ctx, "export update policy", err)
	}
	return connect.NewResponse(&webv1.ExportUpdatePolicyResponse{Yaml: string(p.YAML())}), nil
}

// ResetUpdatePolicy returns a group to the default.
func (h *UpdatePolicyHandler) ResetUpdatePolicy(ctx context.Context, req *connect.Request[webv1.ResetUpdatePolicyRequest]) (*connect.Response[webv1.ResetUpdatePolicyResponse], error) {
	c, group, err := h.callerAndGroup(ctx, req.Msg.GetGroupId())
	if err != nil {
		return nil, err
	}
	if err := h.policies.Reset(ctx, c, group); err != nil {
		return nil, h.fail(ctx, "reset update policy", err)
	}
	return connect.NewResponse(&webv1.ResetUpdatePolicyResponse{}), nil
}

func (h *UpdatePolicyHandler) callerAndGroup(ctx context.Context, raw string) (access.Caller, tenant.GroupID, error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return access.Caller{}, tenant.GroupID{}, err
	}
	group, err := groupOf(raw)
	return c, group, err
}
