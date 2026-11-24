package webapi

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/oidcmapping"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// OIDCMappingHandler implements sermon.web.v1.OidcMappingService (M3-21).
type OIDCMappingHandler struct {
	svc          *oidcmapping.Service
	oidc         bool
	provisioning bool
	logger       *slog.Logger
}

var _ webv1connect.OidcMappingServiceHandler = (*OIDCMappingHandler)(nil)

// NewOIDCMappingHandler returns the handler; oidc and provisioning say what the instance has.
func NewOIDCMappingHandler(svc *oidcmapping.Service, oidc, provisioning bool, logger *slog.Logger) *OIDCMappingHandler {
	return &OIDCMappingHandler{svc: svc, oidc: oidc, provisioning: oidc && provisioning, logger: logger}
}

func mappingView(m oidcmapping.Mapping) *webv1.OidcMapping {
	v := &webv1.OidcMapping{Id: m.ID.String(), OidcGroup: m.OIDCGroup, Role: roleView(m.Role), CreateTime: timestamppb.New(m.CreatedAt)}
	if m.Group != nil {
		v.GroupId = m.Group.String()
	}
	return v
}

// ListOidcMappings lists the organization's mappings.
func (h *OIDCMappingHandler) ListOidcMappings(ctx context.Context, _ *connect.Request[webv1.ListOidcMappingsRequest]) (*connect.Response[webv1.ListOidcMappingsResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	list, err := h.svc.List(ctx, c)
	if err != nil {
		return nil, h.fail(ctx, "list oidc mappings", err)
	}
	res := &webv1.ListOidcMappingsResponse{Oidc: h.oidc, Provisioning: h.provisioning}
	for _, m := range list {
		res.Mappings = append(res.Mappings, mappingView(m))
	}
	return connect.NewResponse(res), nil
}

// CreateOidcMapping maps a provider group to a role.
func (h *OIDCMappingHandler) CreateOidcMapping(ctx context.Context, req *connect.Request[webv1.CreateOidcMappingRequest]) (*connect.Response[webv1.CreateOidcMappingResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	group, err := optionalGroup(req.Msg.GetGroupId())
	if err != nil {
		return nil, err
	}
	m, err := h.svc.Create(ctx, c, req.Msg.GetOidcGroup(), group, roleFrom(req.Msg.GetRole()))
	if err != nil {
		return nil, h.fail(ctx, "create oidc mapping", err)
	}
	return connect.NewResponse(&webv1.CreateOidcMappingResponse{Mapping: mappingView(m)}), nil
}

// DeleteOidcMapping removes a mapping.
func (h *OIDCMappingHandler) DeleteOidcMapping(ctx context.Context, req *connect.Request[webv1.DeleteOidcMappingRequest]) (*connect.Response[webv1.DeleteOidcMappingResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	if err := h.svc.Delete(ctx, c, oidcmapping.ID(id)); err != nil {
		return nil, h.fail(ctx, "delete oidc mapping", err)
	}
	return connect.NewResponse(&webv1.DeleteOidcMappingResponse{}), nil
}

func (h *OIDCMappingHandler) fail(ctx context.Context, op string, err error) error {
	switch {
	case errors.Is(err, access.ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, access.ErrForbidden)
	case errors.Is(err, access.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, access.ErrNotFound)
	case errors.Is(err, access.ErrStepUpRequired):
		return stepUpRequired()
	case errors.Is(err, access.ErrInvalidRole):
		return connect.NewError(connect.CodeInvalidArgument, access.ErrInvalidRole)
	case errors.Is(err, oidcmapping.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, oidcmapping.ErrExists):
		return connect.NewError(connect.CodeAlreadyExists, oidcmapping.ErrExists)
	case errors.Is(err, oidcmapping.ErrTooMany):
		return connect.NewError(connect.CodeResourceExhausted, oidcmapping.ErrTooMany)
	}
	return internalError(ctx, h.logger, op, err)
}
