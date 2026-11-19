package webapi

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/rotation"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// NodeRotator rotates node keys on demand (rotation.Service).
type NodeRotator interface {
	RotateOnDemand(ctx context.Context, c access.Caller, t rotation.Target) (rotation.Result, error)
}

// NodeHandler implements sermon.web.v1.NodeService (behind the Authenticator).
type NodeHandler struct {
	rotator NodeRotator
	logger  *slog.Logger
}

var _ webv1connect.NodeServiceHandler = (*NodeHandler)(nil)

// NewNodeHandler returns the NodeService handler.
func NewNodeHandler(rotator NodeRotator, logger *slog.Logger) *NodeHandler {
	return &NodeHandler{rotator: rotator, logger: logger}
}

// RotateNodeKeys rotates the keys of the selected nodes now.
func (h *NodeHandler) RotateNodeKeys(ctx context.Context, req *connect.Request[webv1.RotateNodeKeysRequest]) (*connect.Response[webv1.RotateNodeKeysResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	var target rotation.Target
	switch t := req.Msg.GetTarget().(type) {
	case *webv1.RotateNodeKeysRequest_Nodes:
		for _, raw := range t.Nodes.GetIds() {
			id, err := parseID(raw)
			if err != nil {
				return nil, err
			}
			target.Nodes = append(target.Nodes, identity.NodeID(id))
		}
	case *webv1.RotateNodeKeysRequest_GroupId:
		id, err := parseID(t.GroupId)
		if err != nil {
			return nil, err
		}
		g := tenant.GroupID(id)
		target.Group = &g
	case *webv1.RotateNodeKeysRequest_Organization:
		target.Organization = true
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("target required"))
	}
	res, err := h.rotator.RotateOnDemand(ctx, c, target)
	switch {
	case errors.Is(err, access.ErrForbidden):
		return nil, connect.NewError(connect.CodePermissionDenied, access.ErrForbidden)
	case errors.Is(err, access.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, access.ErrNotFound)
	case errors.Is(err, access.ErrStepUpRequired):
		return nil, connect.NewError(connect.CodeFailedPrecondition, access.ErrStepUpRequired)
	case err != nil:
		return nil, internalError(ctx, h.logger, "rotate node keys", err)
	}
	return connect.NewResponse(&webv1.RotateNodeKeysResponse{
		Online: int32(res.Online), Offline: int32(res.Offline), //nolint:gosec // G115: at most the number of nodes of an org
	}), nil
}
