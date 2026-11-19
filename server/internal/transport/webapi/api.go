package webapi

import (
	"net/http"
	"net/netip"

	"connectrpc.com/connect"
	"connectrpc.com/validate"

	"github.com/pmoscode/SerMon/server/internal/gen/sermon/agent/v1/agentv1connect"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// Config configures the web API.
type Config struct {
	// AllowedOrigins are the exact origins (scheme://host[:port]) the UI is served from.
	AllowedOrigins []string
	// TrustedProxies may set X-Forwarded-For (e.g. the Caddy container network).
	TrustedProxies []netip.Prefix
}

// Handlers are the web API services.
type Handlers struct {
	Auth         *AuthHandler
	System       *SystemHandler
	Access       *AccessHandler
	Setup        *SetupHandler
	Invitation   *InvitationHandler
	Organization *OrganizationHandler
	Enrollment   *EnrollmentTokenHandler
	Node         *NodeHandler
	AgentCA      *AgentCAHandler
	// AgentEnrollment serves sermon.agent.v1.EnrollmentService for new agents (ADR 0009).
	AgentEnrollment *AgentEnrollmentHandler
	Authenticator   *Authenticator // required for every service except AuthService
}

// Mount registers the web API on mux: protovalidate on every request, security headers, origin
// check and client IP resolution; every service except AuthService requires a session.
func Mount(mux *http.ServeMux, cfg Config, h Handlers) {
	mount := func(path string, handler http.Handler) {
		mux.Handle(path, ClientIP(cfg.TrustedProxies)(RequireOrigin(cfg.AllowedOrigins)(APIHeaders(handler))))
	}
	validation := validate.NewInterceptor()
	// Authentication runs before validation: anonymous callers learn nothing about request rules.
	authenticated := connect.WithInterceptors(h.Authenticator, validation)

	mount(webv1connect.NewAuthServiceHandler(h.Auth, connect.WithInterceptors(validation)))
	mount(webv1connect.NewSetupServiceHandler(h.Setup, connect.WithInterceptors(validation)))
	mount(webv1connect.NewInvitationServiceHandler(h.Invitation, connect.WithInterceptors(validation)))
	mount(webv1connect.NewSystemServiceHandler(h.System, authenticated))
	mount(webv1connect.NewAccessServiceHandler(h.Access, authenticated))
	mount(webv1connect.NewOrganizationServiceHandler(h.Organization, authenticated))
	mount(webv1connect.NewEnrollmentTokenServiceHandler(h.Enrollment, authenticated))
	mount(webv1connect.NewNodeServiceHandler(h.Node, authenticated))
	mount(webv1connect.NewAgentCAServiceHandler(h.AgentCA, authenticated))

	// Agents send neither Origin nor cookies: no origin check (CSRF needs cookies), the
	// enrollment token is the credential (ADR 0009).
	path, handler := agentv1connect.NewEnrollmentServiceHandler(h.AgentEnrollment, connect.WithInterceptors(validation))
	mux.Handle(path, ClientIP(cfg.TrustedProxies)(APIHeaders(handler)))
}
