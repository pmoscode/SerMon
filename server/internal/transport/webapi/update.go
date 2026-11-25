package webapi

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/updates"
	"github.com/pmoscode/SerMon/server/internal/domain/command"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/update"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// UpdateHandler implements sermon.web.v1.UpdateService (behind the Authenticator, M4-09).
type UpdateHandler struct {
	updates *updates.Service
	logger  *slog.Logger
}

var _ webv1connect.UpdateServiceHandler = (*UpdateHandler)(nil)

// NewUpdateHandler returns the UpdateService handler.
func NewUpdateHandler(svc *updates.Service, logger *slog.Logger) *UpdateHandler {
	return &UpdateHandler{updates: svc, logger: logger}
}

func (h *UpdateHandler) failure(ctx context.Context, what string, err error) error {
	switch {
	case errors.Is(err, access.ErrStepUpRequired):
		return stepUpRequired()
	case errors.Is(err, updates.ErrInvalidRequest):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, access.ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, access.ErrForbidden)
	case errors.Is(err, access.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, access.ErrNotFound)
	}
	return internalError(ctx, h.logger, what, err)
}

// GetNodeUpdates returns a visible node's report with its packages.
func (h *UpdateHandler) GetNodeUpdates(ctx context.Context, req *connect.Request[webv1.GetNodeUpdatesRequest]) (*connect.Response[webv1.GetNodeUpdatesResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.Msg.GetNodeId())
	if err != nil {
		return nil, err
	}
	n, err := h.updates.Node(ctx, c, identity.NodeID(id))
	if err != nil {
		return nil, h.failure(ctx, "get node updates", err)
	}
	res := &webv1.GetNodeUpdatesResponse{Node: nodeUpdatesView(n)}
	if n.Report != nil {
		res.Packages = make([]*webv1.UpdatePackage, 0, len(n.Report.Packages))
		for _, p := range n.Report.Packages {
			res.Packages = append(res.Packages, &webv1.UpdatePackage{
				Name: p.Name, InstalledVersion: p.Installed, CandidateVersion: p.Candidate, Source: p.Source,
				Security: p.Security, FirstSeenAt: optionalTimestamp(p.FirstSeenAt),
			})
		}
	}
	return connect.NewResponse(res), nil
}

// RefreshUpdateInfo asks a node's agent for fresh update information.
func (h *UpdateHandler) RefreshUpdateInfo(ctx context.Context, req *connect.Request[webv1.RefreshUpdateInfoRequest]) (*connect.Response[webv1.RefreshUpdateInfoResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.Msg.GetNodeId())
	if err != nil {
		return nil, err
	}
	if err := h.updates.Refresh(ctx, c, identity.NodeID(id)); err != nil {
		if errors.Is(err, updates.ErrTooSoon) {
			return nil, connect.NewError(connect.CodeResourceExhausted, updates.ErrTooSoon)
		}
		return nil, h.failure(ctx, "refresh update info", err)
	}
	return connect.NewResponse(&webv1.RefreshUpdateInfoResponse{}), nil
}

func nodeIDs(raw []string) ([]identity.NodeID, error) {
	out := make([]identity.NodeID, 0, len(raw))
	for _, r := range raw {
		id, err := parseID(r)
		if err != nil {
			return nil, err
		}
		out = append(out, identity.NodeID(id))
	}
	return out, nil
}

// StartUpdates updates nodes by hand.
func (h *UpdateHandler) StartUpdates(ctx context.Context, req *connect.Request[webv1.StartUpdatesRequest]) (*connect.Response[webv1.StartUpdatesResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	ids, err := nodeIDs(req.Msg.GetNodeIds())
	if err != nil {
		return nil, err
	}
	scope := command.ScopeAll
	if req.Msg.GetScope() == webv1.UpdateScope_UPDATE_SCOPE_SECURITY {
		scope = command.ScopeSecurity
	}
	rollouts, err := h.updates.StartUpdates(ctx, c, ids, scope, req.Msg.GetReboot())
	if err != nil {
		return nil, h.failure(ctx, "start updates", err)
	}
	res := &webv1.StartUpdatesResponse{}
	for _, r := range rollouts {
		res.RolloutIds = append(res.RolloutIds, r.ID.String())
	}
	return connect.NewResponse(res), nil
}

// RebootNodes reboots nodes by hand.
func (h *UpdateHandler) RebootNodes(ctx context.Context, req *connect.Request[webv1.RebootNodesRequest]) (*connect.Response[webv1.RebootNodesResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	ids, err := nodeIDs(req.Msg.GetNodeIds())
	if err != nil {
		return nil, err
	}
	commands, err := h.updates.RebootNodes(ctx, c, ids, req.Msg.GetReason())
	if err != nil {
		return nil, h.failure(ctx, "reboot nodes", err)
	}
	res := &webv1.RebootNodesResponse{}
	for _, cmd := range commands {
		res.RunIds = append(res.RunIds, cmd.ID.String())
	}
	return connect.NewResponse(res), nil
}

// ListFleetUpdates returns the visible nodes with their status and the counts per status.
func (h *UpdateHandler) ListFleetUpdates(ctx context.Context, _ *connect.Request[webv1.ListFleetUpdatesRequest]) (*connect.Response[webv1.ListFleetUpdatesResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	fleet, err := h.updates.Fleet(ctx, c)
	if err != nil {
		return nil, h.failure(ctx, "list fleet updates", err)
	}
	count := func(s update.Status) int32 { return int32(min(fleet.Counts[s], 1<<31-1)) } //nolint:gosec // bounded
	res := &webv1.ListFleetUpdatesResponse{
		Nodes: make([]*webv1.NodeUpdates, 0, len(fleet.Nodes)), Unknown: count(update.StatusUnknown),
		Unavailable: count(update.StatusUnavailable), Current: count(update.StatusCurrent),
		WithUpdates: count(update.StatusUpdates), WithSecurity: count(update.StatusSecurity),
		RebootRequired: int32(min(fleet.RebootRequired, 1<<31-1)), //nolint:gosec // bounded
	}
	for _, n := range fleet.Nodes {
		res.Nodes = append(res.Nodes, nodeUpdatesView(n))
	}
	return connect.NewResponse(res), nil
}

var updateStatuses = map[update.Status]webv1.UpdateStatus{
	update.StatusUnknown:     webv1.UpdateStatus_UPDATE_STATUS_UNKNOWN,
	update.StatusUnavailable: webv1.UpdateStatus_UPDATE_STATUS_UNAVAILABLE,
	update.StatusCurrent:     webv1.UpdateStatus_UPDATE_STATUS_CURRENT,
	update.StatusUpdates:     webv1.UpdateStatus_UPDATE_STATUS_UPDATES,
	update.StatusSecurity:    webv1.UpdateStatus_UPDATE_STATUS_SECURITY,
}

// nodeUpdatesView converts a node with its report, without packages. The enum values of
// availability and package manager are the same in the agent's and the web protocol.
func nodeUpdatesView(n updates.NodeUpdates) *webv1.NodeUpdates {
	v := &webv1.NodeUpdates{
		NodeId: n.Node.String(), Hostname: n.Hostname, GroupId: n.Group.String(), Status: updateStatuses[n.Status()],
	}
	r := n.Report
	if r == nil {
		return v
	}
	v.Availability, v.Reason = webv1.UpdateAvailability(r.Availability), r.Reason
	v.PackageManager = webv1.PackageManager(r.Manager)
	v.CollectedAt, v.ReceivedAt = timestamppb.New(r.CollectedAt), optionalTimestamp(n.ReceivedAt)
	v.TotalCount, v.SecurityCount, v.PackagesOmitted = r.Total, r.Security, n.PackagesOmitted
	v.RebootRequired, v.RebootReason, v.Refreshed = r.RebootRequired, r.RebootReason, r.Refreshed
	v.LastUpgradeAt, v.OldestSecuritySince = optionalTime(r.LastUpgradeAt), optionalTime(n.OldestSecurity)
	if p := r.Permissions; p != nil {
		v.Permissions = &webv1.HostUpdatePermissions{RemoteUpdates: p.RemoteUpdates, RemoteReboot: p.RemoteReboot, SelfUpdate: p.SelfUpdate}
	}
	return v
}
