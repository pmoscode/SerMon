package webapi

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// NotificationChannelHandler implements sermon.web.v1.NotificationChannelService (M2-10).
type NotificationChannelHandler struct {
	svc    *notify.Service
	logger *slog.Logger
}

var _ webv1connect.NotificationChannelServiceHandler = (*NotificationChannelHandler)(nil)

// NewNotificationChannelHandler returns the NotificationChannelService handler.
func NewNotificationChannelHandler(svc *notify.Service, logger *slog.Logger) *NotificationChannelHandler {
	return &NotificationChannelHandler{svc: svc, logger: logger}
}

var fieldKinds = map[notify.FieldKind]webv1.ChannelFieldKind{
	notify.FieldText:   webv1.ChannelFieldKind_CHANNEL_FIELD_KIND_TEXT,
	notify.FieldURL:    webv1.ChannelFieldKind_CHANNEL_FIELD_KIND_URL,
	notify.FieldChoice: webv1.ChannelFieldKind_CHANNEL_FIELD_KIND_CHOICE,
	notify.FieldNumber: webv1.ChannelFieldKind_CHANNEL_FIELD_KIND_NUMBER,
}

// ListChannelTypes describes the channel types.
func (h *NotificationChannelHandler) ListChannelTypes(ctx context.Context, _ *connect.Request[webv1.ListChannelTypesRequest]) (*connect.Response[webv1.ListChannelTypesResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	types, err := h.svc.Types(c)
	if err != nil {
		return nil, h.fail(ctx, "list channel types", err)
	}
	res := &webv1.ListChannelTypesResponse{}
	for _, t := range types {
		v := &webv1.ChannelType{Type: string(t.Type)}
		for _, f := range t.Fields {
			v.Fields = append(v.Fields, &webv1.ChannelField{
				Name: f.Name, Kind: fieldKinds[f.Kind], Secret: f.Secret, Required: f.Required, Choices: f.Choices,
				MaxLength: int32(f.MaxLength()), //nolint:gosec // G115: at most a few KiB
			})
		}
		res.Types = append(res.Types, v)
	}
	return connect.NewResponse(res), nil
}

// ListChannels lists the channels.
func (h *NotificationChannelHandler) ListChannels(ctx context.Context, _ *connect.Request[webv1.ListChannelsRequest]) (*connect.Response[webv1.ListChannelsResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	list, err := h.svc.List(ctx, c)
	if err != nil {
		return nil, h.fail(ctx, "list channels", err)
	}
	res := &webv1.ListChannelsResponse{}
	for _, ch := range list {
		res.Channels = append(res.Channels, channelView(ch))
	}
	return connect.NewResponse(res), nil
}

// CreateChannel adds a channel.
func (h *NotificationChannelHandler) CreateChannel(ctx context.Context, req *connect.Request[webv1.CreateChannelRequest]) (*connect.Response[webv1.CreateChannelResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	m := req.Msg
	ch, err := h.svc.Create(ctx, c, notify.NewChannel{
		Type: notify.ChannelType(m.GetType()), Name: m.GetName(), Lang: notify.Lang(m.GetLanguage()), Enabled: m.GetEnabled(),
		Settings: m.GetSettings(), Secrets: m.GetSecrets(),
	})
	if err != nil {
		return nil, h.fail(ctx, "create channel", err)
	}
	return connect.NewResponse(&webv1.CreateChannelResponse{Channel: channelView(ch)}), nil
}

// UpdateChannel changes a channel.
func (h *NotificationChannelHandler) UpdateChannel(ctx context.Context, req *connect.Request[webv1.UpdateChannelRequest]) (*connect.Response[webv1.UpdateChannelResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.Msg.GetChannelId())
	if err != nil {
		return nil, err
	}
	m := req.Msg
	change := notify.ChannelChange{Name: m.Name, Enabled: m.Enabled, Secrets: m.GetSecrets()}
	if m.Language != nil {
		l := notify.Lang(m.GetLanguage())
		change.Lang = &l
	}
	if m.GetReplaceSettings() {
		change.Settings = m.GetSettings()
		if change.Settings == nil {
			change.Settings = map[string]string{}
		}
	}
	ch, err := h.svc.Update(ctx, c, notify.ChannelID(id), change)
	if err != nil {
		return nil, h.fail(ctx, "update channel", err)
	}
	return connect.NewResponse(&webv1.UpdateChannelResponse{Channel: channelView(ch)}), nil
}

// DeleteChannel removes a channel.
func (h *NotificationChannelHandler) DeleteChannel(ctx context.Context, req *connect.Request[webv1.DeleteChannelRequest]) (*connect.Response[webv1.DeleteChannelResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.Msg.GetChannelId())
	if err != nil {
		return nil, err
	}
	if err := h.svc.Delete(ctx, c, notify.ChannelID(id)); err != nil {
		return nil, h.fail(ctx, "delete channel", err)
	}
	return connect.NewResponse(&webv1.DeleteChannelResponse{}), nil
}

// TestChannel sends a test message.
func (h *NotificationChannelHandler) TestChannel(ctx context.Context, req *connect.Request[webv1.TestChannelRequest]) (*connect.Response[webv1.TestChannelResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.Msg.GetChannelId())
	if err != nil {
		return nil, err
	}
	d, err := h.svc.Test(ctx, c, notify.ChannelID(id))
	if err != nil {
		return nil, h.fail(ctx, "test channel", err)
	}
	return connect.NewResponse(&webv1.TestChannelResponse{Delivery: deliveryView(d)}), nil
}

// ListDeliveries returns a page of the delivery log.
func (h *NotificationChannelHandler) ListDeliveries(ctx context.Context, req *connect.Request[webv1.ListDeliveriesRequest]) (*connect.Response[webv1.ListDeliveriesResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	f := notify.DeliveryFilter{Before: req.Msg.GetBeforeId(), Limit: int(req.Msg.GetPageSize())}
	if s := req.Msg.GetChannelId(); s != "" {
		id, err := parseID(s)
		if err != nil {
			return nil, err
		}
		ch := notify.ChannelID(id)
		f.Channel = &ch
	}
	list, err := h.svc.Deliveries(ctx, c, f)
	if err != nil {
		return nil, h.fail(ctx, "list deliveries", err)
	}
	res := &webv1.ListDeliveriesResponse{}
	for _, d := range list {
		res.Deliveries = append(res.Deliveries, deliveryView(d))
	}
	return connect.NewResponse(res), nil
}

func (h *NotificationChannelHandler) fail(ctx context.Context, op string, err error) error {
	switch {
	case errors.Is(err, access.ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, access.ErrForbidden)
	case errors.Is(err, access.ErrStepUpRequired):
		return stepUpRequired()
	case errors.Is(err, access.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, access.ErrNotFound)
	case errors.Is(err, notify.ErrTooManyTests):
		return connect.NewError(connect.CodeResourceExhausted, notify.ErrTooManyTests)
	case errors.Is(err, notify.ErrInvalidChannel), errors.Is(err, notify.ErrUnknownType):
		// The messages name fields, never values (secrets).
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return internalError(ctx, h.logger, op, err)
}

func channelView(c notify.Channel) *webv1.NotificationChannel {
	return &webv1.NotificationChannel{
		Id: c.ID.String(), Type: string(c.Type), Name: c.Name, Language: string(c.Lang), Enabled: c.Enabled,
		Settings: c.Settings, SecretNames: c.SecretNames, CreateTime: timestamppb.New(c.CreatedAt), UpdateTime: timestamppb.New(c.UpdatedAt),
	}
}

var deliveryStatuses = map[notify.Status]webv1.DeliveryStatus{
	notify.StatusPending: webv1.DeliveryStatus_DELIVERY_STATUS_PENDING,
	notify.StatusSending: webv1.DeliveryStatus_DELIVERY_STATUS_SENDING,
	notify.StatusSent:    webv1.DeliveryStatus_DELIVERY_STATUS_SENT,
	notify.StatusFailed:  webv1.DeliveryStatus_DELIVERY_STATUS_FAILED,
}

func optionalTimestamp(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

func deliveryView(d notify.Delivery) *webv1.Delivery {
	v := &webv1.Delivery{
		Id: d.ID, ChannelId: d.Channel.String(), Status: deliveryStatuses[d.Status], Attempts: int32(min(d.Attempts, 100)), //nolint:gosec // G115: bounded
		Test: d.Test, Kind: string(d.Notification.Kind), RuleId: string(d.Notification.Rule), CreateTime: timestamppb.New(d.CreatedAt),
		LastAttemptTime: optionalTimestamp(d.LastAttemptAt),
		SentTime:        optionalTimestamp(d.SentAt),
		Error:           d.LastError,
	}
	if d.Status == notify.StatusPending {
		v.NextAttemptTime = timestamppb.New(d.NextAttemptAt)
	}
	if !d.Test {
		v.NodeId = d.Notification.Node.String()
	}
	return v
}
