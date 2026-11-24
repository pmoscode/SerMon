package webapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

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
	svc       *organization.Service
	lifecycle *organization.Lifecycle
	logger    *slog.Logger
}

// WithLifecycle adds locking and deleting (M2-33).
func (h *OrganizationHandler) WithLifecycle(l *organization.Lifecycle) *OrganizationHandler {
	h.lifecycle = l
	return h
}

// confirmed runs a lifecycle action on the organization of the request.
func (h *OrganizationHandler) confirmed(ctx context.Context, op, id string, run func(access.Caller, tenant.OrgID) error) error {
	c, err := callerFrom(ctx)
	if err != nil {
		return err
	}
	if h.lifecycle == nil {
		return connect.NewError(connect.CodeUnimplemented, errors.New("locking organizations needs the database"))
	}
	org, err := parseID(id)
	if err != nil {
		return err
	}
	if err := run(c, tenant.OrgID(org)); err != nil {
		return h.fail(ctx, op, err)
	}
	return nil
}

// LockOrganization locks an organization.
func (h *OrganizationHandler) LockOrganization(ctx context.Context, req *connect.Request[webv1.LockOrganizationRequest]) (*connect.Response[webv1.LockOrganizationResponse], error) {
	err := h.confirmed(ctx, "lock organization", req.Msg.GetOrganizationId(), func(c access.Caller, org tenant.OrgID) error {
		return h.lifecycle.Lock(ctx, c, org, req.Msg.GetConfirmName())
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&webv1.LockOrganizationResponse{}), nil
}

// ScheduleOrganizationDeletion locks an organization and schedules its deletion.
func (h *OrganizationHandler) ScheduleOrganizationDeletion(ctx context.Context, req *connect.Request[webv1.ScheduleOrganizationDeletionRequest]) (*connect.Response[webv1.ScheduleOrganizationDeletionResponse], error) {
	var due time.Time
	err := h.confirmed(ctx, "schedule deletion", req.Msg.GetOrganizationId(), func(c access.Caller, org tenant.OrgID) error {
		var err error
		due, err = h.lifecycle.ScheduleDeletion(ctx, c, org, req.Msg.GetConfirmName())
		if err != nil {
			return fmt.Errorf("schedule deletion: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&webv1.ScheduleOrganizationDeletionResponse{DeletionTime: timestamppb.New(due)}), nil
}

// UnlockOrganization lifts the lock.
func (h *OrganizationHandler) UnlockOrganization(ctx context.Context, req *connect.Request[webv1.UnlockOrganizationRequest]) (*connect.Response[webv1.UnlockOrganizationResponse], error) {
	err := h.confirmed(ctx, "unlock organization", req.Msg.GetOrganizationId(), func(c access.Caller, org tenant.OrgID) error {
		return h.lifecycle.Unlock(ctx, c, org, req.Msg.GetConfirmName())
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&webv1.UnlockOrganizationResponse{}), nil
}

// DeleteOrganization deletes a locked organization at once.
func (h *OrganizationHandler) DeleteOrganization(ctx context.Context, req *connect.Request[webv1.DeleteOrganizationRequest]) (*connect.Response[webv1.DeleteOrganizationResponse], error) {
	var accounts int64
	err := h.confirmed(ctx, "delete organization", req.Msg.GetOrganizationId(), func(c access.Caller, org tenant.OrgID) error {
		var err error
		accounts, err = h.lifecycle.DeleteNow(ctx, c, org, req.Msg.GetConfirmName())
		if err != nil {
			return fmt.Errorf("delete organization: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&webv1.DeleteOrganizationResponse{AccountsDeleted: accounts}), nil
}

// ExportOrganizationAudit returns the audit log of a locked organization.
func (h *OrganizationHandler) ExportOrganizationAudit(ctx context.Context, req *connect.Request[webv1.ExportOrganizationAuditRequest]) (*connect.Response[webv1.ExportOrganizationAuditResponse], error) {
	res := &webv1.ExportOrganizationAuditResponse{}
	err := h.confirmed(ctx, "export audit log", req.Msg.GetOrganizationId(), func(c access.Caller, org tenant.OrgID) error {
		var err error
		res.FileName, res.Content, res.ChainValid, err = h.lifecycle.ExportAudit(ctx, c, org)
		if err != nil {
			return fmt.Errorf("export audit log: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(res), nil
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
	case errors.Is(err, organization.ErrNameMismatch):
		return connect.NewError(connect.CodeInvalidArgument, organization.ErrNameMismatch)
	case errors.Is(err, organization.ErrNotLocked):
		return connect.NewError(connect.CodeFailedPrecondition, organization.ErrNotLocked)
	case errors.Is(err, organization.ErrLockTooRecent):
		return connect.NewError(connect.CodeFailedPrecondition, organization.ErrLockTooRecent)
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
		NodeCount: o.Nodes, StorageBytes: o.StorageBytes, Status: statusView(o),
		Quotas: quotasView(o.Quotas), LockTime: optionalTime(o.LockedAt), DeletionTime: optionalTime(o.DeletionDueAt),
	}
}

func statusView(o organization.Organization) webv1.OrganizationStatus {
	switch {
	case o.DeletionDueAt != nil:
		return webv1.OrganizationStatus_ORGANIZATION_STATUS_DELETION_SCHEDULED
	case o.LockedAt != nil:
		return webv1.OrganizationStatus_ORGANIZATION_STATUS_LOCKED
	default:
		return webv1.OrganizationStatus_ORGANIZATION_STATUS_ACTIVE
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
