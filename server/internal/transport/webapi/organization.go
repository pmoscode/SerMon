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
	"github.com/pmoscode/SerMon/server/internal/domain/quota"
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
	return connect.NewResponse(&webv1.CreateOrganizationResponse{
		Organization: orgView(org), InvitationToken: token, InvitationEmailed: token == "",
	}), nil
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

// UpdateOrganization sets the name and the quotas of an organization.
func (h *OrganizationHandler) UpdateOrganization(ctx context.Context, req *connect.Request[webv1.UpdateOrganizationRequest]) (*connect.Response[webv1.UpdateOrganizationResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.Msg.GetOrganizationId())
	if err != nil {
		return nil, err
	}
	q := quotasFrom(req.Msg.GetQuotas())
	if err := h.svc.Update(ctx, c, tenant.OrgID(id), req.Msg.GetName(), q); err != nil {
		return nil, h.fail(ctx, "update organization", err)
	}
	orgs, err := h.svc.List(ctx, c)
	if err != nil {
		return nil, h.fail(ctx, "update organization", err)
	}
	res := &webv1.UpdateOrganizationResponse{}
	for _, o := range orgs {
		if o.ID == tenant.OrgID(id) {
			res.Organization = orgView(o)
		}
	}
	return connect.NewResponse(res), nil
}

func (h *OrganizationHandler) fail(ctx context.Context, op string, err error) error {
	switch {
	case errors.Is(err, organization.ErrNameTaken):
		return connect.NewError(connect.CodeAlreadyExists, organization.ErrNameTaken)
	case errors.Is(err, quota.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, quota.ErrInvalid)
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
	case errors.Is(err, invitation.ErrMailFailed):
		return mailFailed(ctx, h.logger, op, err)
	}
	return internalError(ctx, h.logger, op, err)
}

func orgView(o organization.Organization) *webv1.OrganizationSummary {
	return &webv1.OrganizationSummary{
		Id: o.ID.String(), Name: o.Name, CreateTime: timestamppb.New(o.CreatedAt), MemberCount: o.Members,
		NodeCount: o.Nodes, StorageBytes: o.StorageBytes, Status: webv1.OrganizationStatus_ORGANIZATION_STATUS_ACTIVE,
		Quotas: quotasView(o.Quotas),
	}
}

func quotasView(q quota.Quotas) *webv1.OrganizationQuotas {
	return &webv1.OrganizationQuotas{
		MaxNodes: quota.Optional(q.MaxNodes), MaxMembers: quota.Optional(q.MaxMembers), RetentionCapDays: quota.Optional(q.RetentionCapDays),
		SamplesPerSecond: quota.Optional(q.SamplesPerSecond), SeriesPerNode: quota.Optional(q.SeriesPerNode),
	}
}

func quotasFrom(q *webv1.OrganizationQuotas) quota.Quotas {
	return quota.Quotas{
		MaxNodes: q.GetMaxNodes(), MaxMembers: q.GetMaxMembers(), RetentionCapDays: q.GetRetentionCapDays(),
		SamplesPerSecond: q.GetSamplesPerSecond(), SeriesPerNode: q.GetSeriesPerNode(),
	}
}
