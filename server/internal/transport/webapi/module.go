package webapi

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/modules"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/module"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// ModuleHandler implements sermon.web.v1.ModuleService (M5-06, ADR 0039).
type ModuleHandler struct {
	svc    *modules.Service
	logger *slog.Logger
}

var _ webv1connect.ModuleServiceHandler = (*ModuleHandler)(nil)

// NewModuleHandler returns the handler.
func NewModuleHandler(svc *modules.Service, logger *slog.Logger) *ModuleHandler {
	return &ModuleHandler{svc: svc, logger: logger}
}

// ListModules returns the modules the server knows.
func (h *ModuleHandler) ListModules(ctx context.Context, _ *connect.Request[webv1.ListModulesRequest]) (*connect.Response[webv1.ListModulesResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	list, err := h.svc.Modules(ctx, c)
	if err != nil {
		return nil, h.fail(ctx, "list modules", err)
	}
	res := &webv1.ListModulesResponse{}
	for _, m := range list {
		res.Modules = append(res.Modules, &webv1.Module{Id: m.ID, SchemaJson: m.Schema.Raw(), SecretFields: m.Schema.Secrets()})
	}
	return connect.NewResponse(res), nil
}

// GetModuleSettings returns the module settings of every group.
func (h *ModuleHandler) GetModuleSettings(ctx context.Context, _ *connect.Request[webv1.GetModuleSettingsRequest]) (*connect.Response[webv1.GetModuleSettingsResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	view, err := h.svc.View(ctx, c)
	if err != nil {
		return nil, h.fail(ctx, "get module settings", err)
	}
	res := &webv1.GetModuleSettingsResponse{Version: view.Version}
	for _, g := range view.Groups {
		res.Groups = append(res.Groups, &webv1.GroupModuleSettings{GroupId: g.ID.String(), GroupName: g.Name, Modules: settingMessages(g.Modules), Available: g.Available})
	}
	return connect.NewResponse(res), nil
}

// SetGroupModule sets one module of a group.
func (h *ModuleHandler) SetGroupModule(ctx context.Context, req *connect.Request[webv1.SetGroupModuleRequest]) (*connect.Response[webv1.SetGroupModuleResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	group, err := uuid.Parse(req.Msg.GetGroupId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid group id"))
	}
	msg := req.Msg.GetSetting()
	setting := module.Setting{Module: msg.GetModuleId(), Enabled: msg.GetEnabled(), Config: msg.GetConfigJson()}
	if msg.GetInterval() != nil {
		setting.Interval = msg.GetInterval().AsDuration()
	}
	if err := h.svc.SetGroupModule(ctx, c, tenant.GroupID(group), setting); err != nil {
		return nil, h.fail(ctx, "set module", err)
	}
	return connect.NewResponse(&webv1.SetGroupModuleResponse{}), nil
}

// RemoveGroupModule turns a module of a group off.
func (h *ModuleHandler) RemoveGroupModule(ctx context.Context, req *connect.Request[webv1.RemoveGroupModuleRequest]) (*connect.Response[webv1.RemoveGroupModuleResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	group, err := uuid.Parse(req.Msg.GetGroupId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid group id"))
	}
	if err := h.svc.RemoveGroupModule(ctx, c, tenant.GroupID(group), req.Msg.GetModuleId()); err != nil {
		return nil, h.fail(ctx, "remove module", err)
	}
	return connect.NewResponse(&webv1.RemoveGroupModuleResponse{}), nil
}

// GetNodeModules returns the modules of a node.
func (h *ModuleHandler) GetNodeModules(ctx context.Context, req *connect.Request[webv1.GetNodeModulesRequest]) (*connect.Response[webv1.GetNodeModulesResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, err := uuid.Parse(req.Msg.GetNodeId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid node id"))
	}
	n, err := h.svc.Node(ctx, c, identity.NodeID(id))
	if err != nil {
		return nil, h.fail(ctx, "node modules", err)
	}
	res := &webv1.GetNodeModulesResponse{GroupId: n.Group.String(), Builtin: n.Builtin, Vetoed: n.Vetoed, Settings: settingMessages(n.Settings)}
	if n.Health != nil {
		res.HealthCollectedAt = timestamppb.New(n.Health.CollectedAt)
		for _, m := range n.Health.Modules {
			res.Health = append(res.Health, &webv1.ModuleHealth{
				ModuleId: m.Module, State: webv1.ModuleState(m.State), Reason: m.Reason, LastSuccess: optionalTime(m.LastSuccess), // same codes
			})
		}
	}
	if n.Hints != nil {
		res.HintsCollectedAt = timestamppb.New(n.Hints.CollectedAt)
		for _, hint := range n.Hints.Hints {
			res.Hints = append(res.Hints, hintMessage(hint))
		}
	}
	return connect.NewResponse(res), nil
}

// ListModuleHints returns the open suggestions of the caller's nodes.
func (h *ModuleHandler) ListModuleHints(ctx context.Context, _ *connect.Request[webv1.ListModuleHintsRequest]) (*connect.Response[webv1.ListModuleHintsResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	hints, err := h.svc.Hints(ctx, c)
	if err != nil {
		return nil, h.fail(ctx, "module hints", err)
	}
	res := &webv1.ListModuleHintsResponse{}
	for _, nh := range hints {
		res.Hints = append(res.Hints, &webv1.NodeModuleHint{NodeId: nh.Node.String(), Hostname: nh.Hostname, GroupId: nh.Group.String(), Hint: hintMessage(nh.Hint)})
	}
	return connect.NewResponse(res), nil
}

func (h *ModuleHandler) fail(ctx context.Context, op string, err error) error {
	var invalid *module.InvalidError
	switch {
	case errors.Is(err, access.ErrStepUpRequired):
		return stepUpRequired()
	case errors.Is(err, access.ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, access.ErrForbidden)
	case errors.Is(err, access.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, access.ErrNotFound)
	case errors.As(err, &invalid):
		// The admin's own input checked against the module's schema: which property and why.
		return connect.NewError(connect.CodeInvalidArgument, invalid)
	}
	return internalError(ctx, h.logger, op, err)
}

func settingMessages(settings []module.Setting) []*webv1.ModuleSetting {
	out := make([]*webv1.ModuleSetting, 0, len(settings))
	for _, s := range settings {
		msg := &webv1.ModuleSetting{ModuleId: s.Module, Enabled: s.Enabled, ConfigJson: s.Config}
		if s.Interval > 0 {
			msg.Interval = durationpb.New(s.Interval.Truncate(time.Second))
		}
		out = append(out, msg)
	}
	return out
}

func hintMessage(h module.Hint) *webv1.ModuleHint {
	return &webv1.ModuleHint{ModuleId: h.Module, Reasons: h.Reasons, SuggestedConfigJson: h.Suggested}
}
