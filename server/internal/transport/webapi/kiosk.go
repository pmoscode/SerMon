package webapi

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/kiosk"
	"github.com/pmoscode/SerMon/server/internal/domain/dashboard"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// KioskTokenHandler implements sermon.web.v1.KioskTokenService (M5-26, behind the
// Authenticator).
type KioskTokenHandler struct {
	svc    *kiosk.Service
	logger *slog.Logger
}

var _ webv1connect.KioskTokenServiceHandler = (*KioskTokenHandler)(nil)

// NewKioskTokenHandler returns the KioskTokenService handler.
func NewKioskTokenHandler(svc *kiosk.Service, logger *slog.Logger) *KioskTokenHandler {
	return &KioskTokenHandler{svc: svc, logger: logger}
}

func kioskTokenView(t kiosk.Token) *webv1.KioskToken {
	return &webv1.KioskToken{
		Id: t.ID.String(), DashboardId: t.Dashboard.String(), Name: t.Name, CreatedByName: t.CreatedByName,
		CreateTime: timestamppb.New(t.CreatedAt), ExpireTime: timestamppb.New(t.ExpiresAt),
		LastUseTime: optionalTime(t.LastUsedAt), RevokeTime: optionalTime(t.RevokedAt),
	}
}

// ListKioskTokens lists the tokens of a dashboard.
func (h *KioskTokenHandler) ListKioskTokens(ctx context.Context, req *connect.Request[webv1.ListKioskTokensRequest]) (*connect.Response[webv1.ListKioskTokensResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, err := dashboardID(req.Msg.GetDashboardId())
	if err != nil {
		return nil, err
	}
	list, err := h.svc.List(ctx, c, id)
	if err != nil {
		return nil, kioskFail(ctx, h.logger, "list kiosk tokens", err)
	}
	res := &webv1.ListKioskTokensResponse{}
	for _, t := range list {
		res.Tokens = append(res.Tokens, kioskTokenView(t))
	}
	return connect.NewResponse(res), nil
}

// CreateKioskToken adds a token and returns its secret once.
func (h *KioskTokenHandler) CreateKioskToken(ctx context.Context, req *connect.Request[webv1.CreateKioskTokenRequest]) (*connect.Response[webv1.CreateKioskTokenResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, err := dashboardID(req.Msg.GetDashboardId())
	if err != nil {
		return nil, err
	}
	t, secret, err := h.svc.Create(ctx, c, id, req.Msg.GetName(), req.Msg.GetValidFor().AsDuration())
	if err != nil {
		return nil, kioskFail(ctx, h.logger, "create kiosk token", err)
	}
	return connect.NewResponse(&webv1.CreateKioskTokenResponse{Token: kioskTokenView(t), Secret: secret}), nil
}

// RevokeKioskToken ends a token.
func (h *KioskTokenHandler) RevokeKioskToken(ctx context.Context, req *connect.Request[webv1.RevokeKioskTokenRequest]) (*connect.Response[webv1.RevokeKioskTokenResponse], error) {
	c, err := callerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.Msg.GetTokenId())
	if err != nil {
		return nil, err
	}
	if err := h.svc.Revoke(ctx, c, kiosk.TokenID(id)); err != nil {
		return nil, kioskFail(ctx, h.logger, "revoke kiosk token", err)
	}
	return connect.NewResponse(&webv1.RevokeKioskTokenResponse{}), nil
}

func kioskFail(ctx context.Context, logger *slog.Logger, op string, err error) error {
	switch {
	case errors.Is(err, kiosk.ErrInvalidToken):
		return connect.NewError(connect.CodeUnauthenticated, kiosk.ErrInvalidToken)
	case errors.Is(err, access.ErrStepUpRequired):
		return stepUpRequired()
	case errors.Is(err, kiosk.ErrNotShared):
		return connect.NewError(connect.CodeFailedPrecondition, kiosk.ErrNotShared)
	case errors.Is(err, kiosk.ErrTooMany):
		return connect.NewError(connect.CodeResourceExhausted, kiosk.ErrTooMany)
	case errors.Is(err, kiosk.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, unwrapTo(err, kiosk.ErrInvalid))
	}
	return readFailure(ctx, logger, op, err)
}

// KioskAuthorization is the scheme of the kiosk token in the Authorization header.
const KioskAuthorization = "Kiosk "

// Limits of the displays (T115): per client address for every call, per token after checking it.
const (
	KioskCallsPerIP    = 600
	KioskCallsPerToken = 240
)

// kioskLimiter is satisfied by ratelimit.Limiter.
type kioskLimiter interface{ Allow(key string) bool }

var errTooManyKioskCalls = errors.New("too many requests of the display, wait a moment")

type kioskKey struct{}

// KioskAuthenticator is the interceptor of the KioskService: the token of the Authorization
// header is the only credential (no cookie, no session), every call is rate-limited.
type KioskAuthenticator struct {
	svc             *kiosk.Service
	perIP, perToken kioskLimiter
	logger          *slog.Logger
}

// NewKioskAuthenticator returns the interceptor.
func NewKioskAuthenticator(svc *kiosk.Service, perIP, perToken kioskLimiter, logger *slog.Logger) *KioskAuthenticator {
	return &KioskAuthenticator{svc: svc, perIP: perIP, perToken: perToken, logger: logger}
}

// WrapUnary implements connect.Interceptor.
func (a *KioskAuthenticator) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if !a.perIP.Allow(ClientIPFrom(ctx)) {
			return nil, connect.NewError(connect.CodeResourceExhausted, errTooManyKioskCalls)
		}
		secret, ok := strings.CutPrefix(req.Header().Get("Authorization"), KioskAuthorization)
		if !ok {
			return nil, connect.NewError(connect.CodeUnauthenticated, kiosk.ErrInvalidToken)
		}
		d, err := a.svc.Authenticate(ctx, secret)
		if err != nil {
			return nil, kioskFail(ctx, a.logger, "kiosk token", err)
		}
		if !a.perToken.Allow(d.Token.String()) {
			return nil, connect.NewError(connect.CodeResourceExhausted, errTooManyKioskCalls)
		}
		return next(context.WithValue(ctx, kioskKey{}, d), req)
	}
}

// WrapStreamingClient implements connect.Interceptor (unused on the server).
func (a *KioskAuthenticator) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler implements connect.Interceptor: the KioskService has no streams.
func (a *KioskAuthenticator) WrapStreamingHandler(connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(context.Context, connect.StreamingHandlerConn) error {
		return connect.NewError(connect.CodeUnauthenticated, kiosk.ErrInvalidToken)
	}
}

func displayFrom(ctx context.Context) (kiosk.Display, error) {
	d, ok := ctx.Value(kioskKey{}).(kiosk.Display)
	if !ok {
		return kiosk.Display{}, connect.NewError(connect.CodeUnauthenticated, kiosk.ErrInvalidToken)
	}
	return d, nil
}

// KioskHandler implements sermon.web.v1.KioskService (behind the KioskAuthenticator only).
type KioskHandler struct {
	svc    *kiosk.Service
	logger *slog.Logger
}

var _ webv1connect.KioskServiceHandler = (*KioskHandler)(nil)

// NewKioskHandler returns the KioskService handler.
func NewKioskHandler(svc *kiosk.Service, logger *slog.Logger) *KioskHandler {
	return &KioskHandler{svc: svc, logger: logger}
}

// GetKioskDashboard returns the dashboard of the token.
func (h *KioskHandler) GetKioskDashboard(ctx context.Context, _ *connect.Request[webv1.GetKioskDashboardRequest]) (*connect.Response[webv1.GetKioskDashboardResponse], error) {
	d, err := displayFrom(ctx)
	if err != nil {
		return nil, err
	}
	b, err := h.svc.Dashboard(ctx, d)
	if err != nil {
		return nil, kioskFail(ctx, h.logger, "kiosk dashboard", err)
	}
	return connect.NewResponse(&webv1.GetKioskDashboardResponse{DashboardId: b.ID.String(), Spec: specView(b.Spec), Revision: b.Revision}), nil
}

// QueryKioskWidget returns the data of a widget.
func (h *KioskHandler) QueryKioskWidget(ctx context.Context, req *connect.Request[webv1.QueryKioskWidgetRequest]) (*connect.Response[webv1.QueryKioskWidgetResponse], error) {
	d, err := displayFrom(ctx)
	if err != nil {
		return nil, err
	}
	r, err := h.svc.Widget(ctx, d, req.Msg.GetWidgetId())
	if err != nil {
		return nil, kioskFail(ctx, h.logger, "kiosk widget", err)
	}
	return connect.NewResponse(kioskResultView(r)), nil
}

func kioskResultView(r kiosk.Result) *webv1.QueryKioskWidgetResponse {
	res := &webv1.QueryKioskWidgetResponse{From: timestamppb.New(r.From), To: timestamppb.New(r.To)}
	switch r.Kind {
	case dashboard.KindLogStream:
		logs := &webv1.KioskLogs{}
		if r.Logs != nil {
			logs.More = r.Logs.More
			for _, l := range r.Logs.Lines {
				logs.Lines = append(logs.Lines, logLineMessage(l))
			}
		}
		res.Data = &webv1.QueryKioskWidgetResponse_Logs{Logs: logs}
	case dashboard.KindAlertList:
		list := &webv1.KioskAlerts{}
		for _, a := range r.Alerts {
			list.Alerts = append(list.Alerts, alertView(a))
		}
		res.Data = &webv1.QueryKioskWidgetResponse_Alerts{Alerts: list}
	case dashboard.KindTimeseries, dashboard.KindGauge, dashboard.KindStat, dashboard.KindTable:
		m := &webv1.KioskMetrics{Unit: string(r.Unit), Series: seriesViews(r.Series), Hostnames: map[string]string{}}
		for _, s := range r.Series {
			if name, ok := r.Hostnames[s.Node]; ok {
				m.Hostnames[s.Node.String()] = name
			}
		}
		res.Data = &webv1.QueryKioskWidgetResponse_Metrics{Metrics: m}
	}
	return res
}
