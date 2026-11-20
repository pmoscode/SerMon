package webapi

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/invitation"
	"github.com/pmoscode/SerMon/server/internal/app/organization"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// OrganizationHandler implements sermon.web.v1.OrganizationService (behind the Authenticator).
type OrganizationHandler struct {
	svc    *organization.Service
	logger *slog.Logger
}

var _ webv1connect.OrganizationServiceHandler = (*OrganizationHandler)(nil)

// NewOrganizationHandler returns the OrganizationService handler.
func NewOrganizationHandler(svc *organization.Service, logger *slog.Logger) *OrganizationHandler {
	return &OrganizationHandler{svc: svc, logger: logger}
}

// ListOrganizations lists all organizations.
func (h *OrganizationHandler) ListOrganizations(ctx context.Context, _ *connect.Request[webv1.ListOrganizationsRequest]) (*connect.Response[webv1.ListOrganizationsResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	orgs, err := h.svc.List(ctx, c)
	if err != nil {
		return nil, h.fail(ctx, "list organizations", err)
	}
	res := &webv1.ListOrganizationsResponse{}
	for _, o := range orgs {
		res.Organizations = append(res.Organizations, orgView(o))
	}
	return connect.NewResponse(res), nil
}

// CreateOrganization creates an organization and invites its first admin.
func (h *OrganizationHandler) CreateOrganization(ctx context.Context, req *connect.Request[webv1.CreateOrganizationRequest]) (*connect.Response[webv1.CreateOrganizationResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	org, token, err := h.svc.Create(ctx, c, req.Msg.GetName(), req.Msg.GetAdminEmail())
	if err != nil {
		return nil, h.fail(ctx, "create organization", err)
	}
	return connect.NewResponse(&webv1.CreateOrganizationResponse{Organization: orgView(org), InvitationToken: token}), nil
}

// RenameOrganization renames an organization.
func (h *OrganizationHandler) RenameOrganization(ctx context.Context, req *connect.Request[webv1.RenameOrganizationRequest]) (*connect.Response[webv1.RenameOrganizationResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.Msg.GetOrganizationId())
	if err != nil {
		return nil, err
	}
	if err := h.svc.Rename(ctx, c, tenant.OrgID(id), req.Msg.GetName()); err != nil {
		return nil, h.fail(ctx, "rename organization", err)
	}
	return connect.NewResponse(&webv1.RenameOrganizationResponse{}), nil
}

func (h *OrganizationHandler) fail(ctx context.Context, op string, err error) error {
	switch {
	case errors.Is(err, access.ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, access.ErrForbidden)
	case errors.Is(err, access.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, access.ErrNotFound)
	case errors.Is(err, access.ErrStepUpRequired):
		return stepUpRequired()
	case errors.Is(err, organization.ErrInvalidName):
		return connect.NewError(connect.CodeInvalidArgument, organization.ErrInvalidName)
	case errors.Is(err, invitation.ErrInvalidEmail):
		return connect.NewError(connect.CodeInvalidArgument, invitation.ErrInvalidEmail)
	}
	return internalError(ctx, h.logger, op, err)
}

func orgView(o organization.Organization) *webv1.OrganizationSummary {
	return &webv1.OrganizationSummary{Id: o.ID.String(), Name: o.Name, CreateTime: timestamppb.New(o.CreatedAt), MemberCount: o.Members}
}
