package webapi

import (
	"net/http"
	"net/netip"

	"connectrpc.com/connect"
	"connectrpc.com/validate"

	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// Config configures the web API.
type Config struct {
	// AllowedOrigins are the exact origins (scheme://host[:port]) the UI is served from.
	AllowedOrigins []string
	// TrustedProxies may set X-Forwarded-For (e.g. the Caddy container network).
	TrustedProxies []netip.Prefix
}

// Mount registers the web API on mux: protovalidate on every request, security headers, origin
// check and client IP resolution.
func Mount(mux *http.ServeMux, cfg Config, auth *AuthHandler) {
	path, handler := webv1connect.NewAuthServiceHandler(auth, connect.WithInterceptors(validate.NewInterceptor()))
	mux.Handle(path, ClientIP(cfg.TrustedProxies)(RequireOrigin(cfg.AllowedOrigins)(APIHeaders(handler))))
}
