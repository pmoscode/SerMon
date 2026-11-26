package webapi

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	"github.com/pmoscode/SerMon/server/internal/app/orgretention"
	"github.com/pmoscode/SerMon/server/internal/domain/retention"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
)

// WithRetention enables GetRetention and SetRetention (M5-30).
func (h *AccessHandler) WithRetention(svc *orgretention.Service) *AccessHandler {
	h.retention = svc
	return h
}

// GetRetention returns the organization's retention with the limits.
func (h *AccessHandler) GetRetention(ctx context.Context, _ *connect.Request[webv1.GetRetentionRequest]) (*connect.Response[webv1.GetRetentionResponse], error) {
	if h.retention == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("retention is not configured"))
	}
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	view, err := h.retention.Get(ctx, c)
	if err != nil {
		return nil, h.fail(ctx, "get retention", err)
	}
	return connect.NewResponse(&webv1.GetRetentionResponse{
		LogRetentionDays: view.Settings.LogDays, EventRetentionDays: view.Settings.EventDays,
		LogMinDays: retention.MinLogDays, LogMaxDays: retention.MaxLogDays,
		EventMinDays: retention.MinEventDays, EventMaxDays: retention.MaxEventDays, CapDays: view.CapDays,
	}), nil
}

// SetRetention changes the organization's retention.
func (h *AccessHandler) SetRetention(ctx context.Context, req *connect.Request[webv1.SetRetentionRequest]) (*connect.Response[webv1.SetRetentionResponse], error) {
	if h.retention == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("retention is not configured"))
	}
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	set := retention.Settings{LogDays: req.Msg.GetLogRetentionDays(), EventDays: req.Msg.GetEventRetentionDays()}
	if err := h.retention.Set(ctx, c, set); err != nil {
		if errors.Is(err, retention.ErrInvalid) {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		return nil, h.fail(ctx, "set retention", err)
	}
	return connect.NewResponse(&webv1.SetRetentionResponse{}), nil
}
