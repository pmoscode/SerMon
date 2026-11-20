package webapi

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"

	"github.com/pmoscode/SerMon/server/internal/app/instanceaudit"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// InstanceAuditHandler implements sermon.web.v1.InstanceAuditService (behind the Authenticator).
type InstanceAuditHandler struct {
	svc    *instanceaudit.Service
	logger *slog.Logger
}

var _ webv1connect.InstanceAuditServiceHandler = (*InstanceAuditHandler)(nil)

// NewInstanceAuditHandler returns the InstanceAuditService handler.
func NewInstanceAuditHandler(svc *instanceaudit.Service, logger *slog.Logger) *InstanceAuditHandler {
	return &InstanceAuditHandler{svc: svc, logger: logger}
}

// ListInstanceAuditLog returns a page of the instance-wide audit log.
func (h *InstanceAuditHandler) ListInstanceAuditLog(ctx context.Context, req *connect.Request[webv1.ListInstanceAuditLogRequest]) (*connect.Response[webv1.ListInstanceAuditLogResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	records, err := h.svc.List(ctx, c, req.Msg.GetBeforeSeq(), int(req.Msg.GetPageSize()))
	if err != nil {
		return nil, readFailure(ctx, h.logger, "list instance audit log", err)
	}
	return connect.NewResponse(&webv1.ListInstanceAuditLogResponse{Entries: auditEntries(records)}), nil
}
