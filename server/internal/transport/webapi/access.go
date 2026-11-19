package webapi

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// AccessHandler implements sermon.web.v1.AccessService (mounted behind the Authenticator).
type AccessHandler struct {
	svc    *access.Service
	logger *slog.Logger
}

var _ webv1connect.AccessServiceHandler = (*AccessHandler)(nil)

// NewAccessHandler returns the AccessService handler.
func NewAccessHandler(svc *access.Service, logger *slog.Logger) *AccessHandler {
	return &AccessHandler{svc: svc, logger: logger}
}

// ListNodeGroups lists the visible node groups.
func (h *AccessHandler) ListNodeGroups(ctx context.Context, _ *connect.Request[webv1.ListNodeGroupsRequest]) (*connect.Response[webv1.ListNodeGroupsResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	groups, err := h.svc.ListGroups(ctx, c)
	if err != nil {
		return nil, h.fail(ctx, "list node groups", err)
	}
	res := &webv1.ListNodeGroupsResponse{}
	for _, g := range groups {
		res.Groups = append(res.Groups, groupView(g))
	}
	return connect.NewResponse(res), nil
}

// CreateNodeGroup creates a node group.
func (h *AccessHandler) CreateNodeGroup(ctx context.Context, req *connect.Request[webv1.CreateNodeGroupRequest]) (*connect.Response[webv1.CreateNodeGroupResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	g, err := h.svc.CreateGroup(ctx, c, req.Msg.GetName())
	if err != nil {
		return nil, h.fail(ctx, "create node group", err)
	}
	return connect.NewResponse(&webv1.CreateNodeGroupResponse{Group: groupView(g)}), nil
}

// RenameNodeGroup renames a node group.
func (h *AccessHandler) RenameNodeGroup(ctx context.Context, req *connect.Request[webv1.RenameNodeGroupRequest]) (*connect.Response[webv1.RenameNodeGroupResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.Msg.GetGroupId())
	if err != nil {
		return nil, err
	}
	if err := h.svc.RenameGroup(ctx, c, tenant.GroupID(id), req.Msg.GetName()); err != nil {
		return nil, h.fail(ctx, "rename node group", err)
	}
	return connect.NewResponse(&webv1.RenameNodeGroupResponse{}), nil
}

// DeleteNodeGroup deletes a node group.
func (h *AccessHandler) DeleteNodeGroup(ctx context.Context, req *connect.Request[webv1.DeleteNodeGroupRequest]) (*connect.Response[webv1.DeleteNodeGroupResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.Msg.GetGroupId())
	if err != nil {
		return nil, err
	}
	if err := h.svc.DeleteGroup(ctx, c, tenant.GroupID(id)); err != nil {
		return nil, h.fail(ctx, "delete node group", err)
	}
	return connect.NewResponse(&webv1.DeleteNodeGroupResponse{}), nil
}

// ListMembers lists the members with their roles.
func (h *AccessHandler) ListMembers(ctx context.Context, _ *connect.Request[webv1.ListMembersRequest]) (*connect.Response[webv1.ListMembersResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	members, err := h.svc.ListMembers(ctx, c)
	if err != nil {
		return nil, h.fail(ctx, "list members", err)
	}
	res := &webv1.ListMembersResponse{}
	for _, m := range members {
		view := &webv1.Member{UserId: m.User.String(), Email: m.Email, DisplayName: m.DisplayName}
		for _, b := range m.Bindings {
			rb := &webv1.RoleBinding{Id: b.ID.String(), Role: roleView(b.Role)}
			if b.Group != nil {
				rb.GroupId = b.Group.String()
			}
			view.Bindings = append(view.Bindings, rb)
		}
		res.Members = append(res.Members, view)
	}
	return connect.NewResponse(res), nil
}

// GrantRole grants a role.
func (h *AccessHandler) GrantRole(ctx context.Context, req *connect.Request[webv1.GrantRoleRequest]) (*connect.Response[webv1.GrantRoleResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	user, err := parseID(req.Msg.GetUserId())
	if err != nil {
		return nil, err
	}
	var group *tenant.GroupID
	if req.Msg.GetGroupId() != "" {
		id, err := parseID(req.Msg.GetGroupId())
		if err != nil {
			return nil, err
		}
		g := tenant.GroupID(id)
		group = &g
	}
	id, err := h.svc.GrantRole(ctx, c, identity.UserID(user), group, roleFrom(req.Msg.GetRole()))
	if err != nil {
		return nil, h.fail(ctx, "grant role", err)
	}
	return connect.NewResponse(&webv1.GrantRoleResponse{BindingId: id.String()}), nil
}

// RevokeRole revokes a role binding.
func (h *AccessHandler) RevokeRole(ctx context.Context, req *connect.Request[webv1.RevokeRoleRequest]) (*connect.Response[webv1.RevokeRoleResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.Msg.GetBindingId())
	if err != nil {
		return nil, err
	}
	if err := h.svc.RevokeRole(ctx, c, access.BindingID(id)); err != nil {
		return nil, h.fail(ctx, "revoke role", err)
	}
	return connect.NewResponse(&webv1.RevokeRoleResponse{}), nil
}

// RemoveMember removes a member.
func (h *AccessHandler) RemoveMember(ctx context.Context, req *connect.Request[webv1.RemoveMemberRequest]) (*connect.Response[webv1.RemoveMemberResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	user, err := parseID(req.Msg.GetUserId())
	if err != nil {
		return nil, err
	}
	if err := h.svc.RemoveMember(ctx, c, identity.UserID(user)); err != nil {
		return nil, h.fail(ctx, "remove member", err)
	}
	return connect.NewResponse(&webv1.RemoveMemberResponse{}), nil
}

// ListAuditLog returns a page of the audit log.
func (h *AccessHandler) ListAuditLog(ctx context.Context, req *connect.Request[webv1.ListAuditLogRequest]) (*connect.Response[webv1.ListAuditLogResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	records, err := h.svc.ListAudit(ctx, c, req.Msg.GetBeforeSeq(), int(req.Msg.GetPageSize()))
	if err != nil {
		return nil, h.fail(ctx, "list audit log", err)
	}
	res := &webv1.ListAuditLogResponse{}
	for _, r := range records {
		e := &webv1.AuditEntry{Seq: r.Seq, Time: timestamppb.New(r.At), Action: r.Action, Target: r.Target, Details: r.Details}
		if r.Actor != nil {
			e.ActorId = r.Actor.String()
		}
		res.Entries = append(res.Entries, e)
	}
	return connect.NewResponse(res), nil
}

// fail maps use-case errors to Connect codes; unexpected errors are logged and generic.
func (h *AccessHandler) fail(ctx context.Context, op string, err error) error {
	switch {
	case errors.Is(err, access.ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, access.ErrForbidden)
	case errors.Is(err, access.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, access.ErrNotFound)
	case errors.Is(err, access.ErrConflict):
		return connect.NewError(connect.CodeAlreadyExists, access.ErrConflict)
	case errors.Is(err, access.ErrStepUpRequired):
		return connect.NewError(connect.CodeFailedPrecondition, access.ErrStepUpRequired)
	case errors.Is(err, access.ErrLastAdmin):
		return connect.NewError(connect.CodeFailedPrecondition, access.ErrLastAdmin)
	case errors.Is(err, access.ErrInvalidName):
		return connect.NewError(connect.CodeInvalidArgument, access.ErrInvalidName)
	case errors.Is(err, access.ErrInvalidRole):
		return connect.NewError(connect.CodeInvalidArgument, access.ErrInvalidRole)
	}
	return internalError(ctx, h.logger, op, err)
}

// callerFrom returns the caller set by the Authenticator.
func callerFrom(ctx context.Context) (access.Caller, error) {
	p, okP := PrincipalFrom(ctx)
	s, okS := sessionFrom(ctx)
	if !okP || !okS {
		return access.Caller{}, connect.NewError(connect.CodeUnauthenticated, errUnauthenticated)
	}
	return access.Caller{Principal: p, StepUpAt: s.StepUpAt}, nil
}

var errInvalidID = errors.New("invalid id")

func parseID(s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil || id == uuid.Nil {
		return uuid.UUID{}, connect.NewError(connect.CodeInvalidArgument, errInvalidID)
	}
	return id, nil
}

func groupView(g access.Group) *webv1.NodeGroup {
	return &webv1.NodeGroup{Id: g.ID.String(), Name: g.Name, CreateTime: timestamppb.New(g.CreatedAt)}
}

func roleView(r role.Role) webv1.Role {
	switch r {
	case role.Viewer:
		return webv1.Role_ROLE_VIEWER
	case role.Operator:
		return webv1.Role_ROLE_OPERATOR
	case role.Admin:
		return webv1.Role_ROLE_ADMIN
	}
	return webv1.Role_ROLE_UNSPECIFIED
}

func roleFrom(r webv1.Role) role.Role {
	switch r {
	case webv1.Role_ROLE_VIEWER:
		return role.Viewer
	case webv1.Role_ROLE_OPERATOR:
		return role.Operator
	case webv1.Role_ROLE_ADMIN:
		return role.Admin
	case webv1.Role_ROLE_UNSPECIFIED:
	}
	return 0
}
