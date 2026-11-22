package webapi

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	"github.com/pmoscode/SerMon/server/internal/app/rotation"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tag"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// NodeRotator rotates node keys on demand (rotation.Service).
type NodeRotator interface {
	RotateOnDemand(ctx context.Context, c access.Caller, t rotation.Target) (rotation.Result, error)
	UnlockNode(ctx context.Context, c access.Caller, node identity.NodeID) error
	AllowNodeTakeover(ctx context.Context, c access.Caller, node identity.NodeID) (time.Time, error)
}

// NodeTagger sets the tags of nodes (fleet.Tagger, M2-29).
type NodeTagger interface {
	SetTags(ctx context.Context, c access.Caller, node identity.NodeID, tags tag.Tags) error
}

// NodeHandler implements sermon.web.v1.NodeService (behind the Authenticator).
type NodeHandler struct {
	rotator NodeRotator
	fleet   *fleet.Service
	tagger  NodeTagger
	logger  *slog.Logger
}

var _ webv1connect.NodeServiceHandler = (*NodeHandler)(nil)

// NewNodeHandler returns the NodeService handler.
func NewNodeHandler(rotator NodeRotator, nodes *fleet.Service, logger *slog.Logger) *NodeHandler {
	return &NodeHandler{rotator: rotator, fleet: nodes, logger: logger}
}

// WithTagger enables SetNodeTags.
func (h *NodeHandler) WithTagger(t NodeTagger) *NodeHandler {
	h.tagger = t
	return h
}

// SetNodeTags replaces the tags of a node.
func (h *NodeHandler) SetNodeTags(ctx context.Context, req *connect.Request[webv1.SetNodeTagsRequest]) (*connect.Response[webv1.SetNodeTagsResponse], error) {
	if h.tagger == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("node tags are not configured"))
	}
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.Msg.GetNodeId())
	if err != nil {
		return nil, err
	}
	if err := h.tagger.SetTags(ctx, c, identity.NodeID(id), tag.Tags(req.Msg.GetTags())); err != nil {
		if errors.Is(err, tag.ErrInvalid) {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		return nil, h.identityFailure(ctx, "set node tags", err)
	}
	return connect.NewResponse(&webv1.SetNodeTagsResponse{}), nil
}

// identityFailure maps the errors of the node identity use cases.
func (h *NodeHandler) identityFailure(ctx context.Context, op string, err error) error {
	switch {
	case errors.Is(err, access.ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, access.ErrForbidden)
	case errors.Is(err, access.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, access.ErrNotFound)
	case errors.Is(err, access.ErrStepUpRequired):
		return stepUpRequired()
	default:
		return internalError(ctx, h.logger, op, err)
	}
}

// UnlockNode lifts the lock of a node.
func (h *NodeHandler) UnlockNode(ctx context.Context, req *connect.Request[webv1.UnlockNodeRequest]) (*connect.Response[webv1.UnlockNodeResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.Msg.GetNodeId())
	if err != nil {
		return nil, err
	}
	if err := h.rotator.UnlockNode(ctx, c, identity.NodeID(id)); err != nil {
		return nil, h.identityFailure(ctx, "unlock node", err)
	}
	return connect.NewResponse(&webv1.UnlockNodeResponse{}), nil
}

// AllowNodeTakeover opens the takeover window of a node.
func (h *NodeHandler) AllowNodeTakeover(ctx context.Context, req *connect.Request[webv1.AllowNodeTakeoverRequest]) (*connect.Response[webv1.AllowNodeTakeoverResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.Msg.GetNodeId())
	if err != nil {
		return nil, err
	}
	until, err := h.rotator.AllowNodeTakeover(ctx, c, identity.NodeID(id))
	if err != nil {
		return nil, h.identityFailure(ctx, "allow node takeover", err)
	}
	return connect.NewResponse(&webv1.AllowNodeTakeoverResponse{Until: timestamppb.New(until)}), nil
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
	target.RevokeOnly = req.Msg.GetRevokeOnly()
	res, err := h.rotator.RotateOnDemand(ctx, c, target)
	switch {
	case errors.Is(err, access.ErrForbidden):
		return nil, connect.NewError(connect.CodePermissionDenied, access.ErrForbidden)
	case errors.Is(err, access.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, access.ErrNotFound)
	case errors.Is(err, access.ErrStepUpRequired):
		return nil, stepUpRequired()
	case err != nil:
		return nil, internalError(ctx, h.logger, "rotate node keys", err)
	}
	return connect.NewResponse(&webv1.RotateNodeKeysResponse{
		//nolint:gosec // G115: at most the number of nodes of an org
		Online: int32(res.Online), Offline: int32(res.Offline), Revoked: int32(res.Revoked),
	}), nil
}
