package agentapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/validate"

	"github.com/pmoscode/SerMon/server/internal/adapters/pki"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	agentv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/agent/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/agent/v1/agentv1connect"
)

// Identity is the agent identity from the verified client certificate – the only source of
// organization and node for everything the agent sends (CLAUDE.md security guardrails).
type Identity struct {
	Org  tenant.OrgID
	Node identity.NodeID
}

type identityKey struct{}

// IdentityFrom returns the agent identity of the request.
func IdentityFrom(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok
}

// withIdentity puts the identity of the client certificate (verified during the TLS handshake)
// into the request context.
func withIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			http.Error(w, "client certificate required", http.StatusUnauthorized)
			return
		}
		org, node, err := pki.AgentIdentity(r.TLS.PeerCertificates[0])
		if err != nil {
			http.Error(w, "invalid agent identity", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, Identity{Org: org, Node: node})))
	})
}

// Mount registers the agent API on mux (served only on the mTLS listener).
func Mount(mux *http.ServeMux, sessions *SessionHandler) {
	path, handler := agentv1connect.NewAgentServiceHandler(sessions, connect.WithInterceptors(validate.NewInterceptor()))
	mux.Handle(path, withIdentity(handler))
}

// ActiveSession describes a connected agent.
type ActiveSession struct {
	Org          tenant.OrgID
	Node         identity.NodeID
	AgentVersion string
	Hostname     string
	Since        time.Time
}

// Sessions tracks connected agents; a node has at most one active session (KONZEPT §6).
type Sessions struct {
	mu     sync.Mutex
	active map[identity.NodeID]ActiveSession
}

// NewSessions returns an empty registry.
func NewSessions() *Sessions { return &Sessions{active: map[identity.NodeID]ActiveSession{}} }

// Active returns the connected agents.
func (s *Sessions) Active() []ActiveSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := make([]ActiveSession, 0, len(s.active))
	for _, a := range s.active {
		list = append(list, a)
	}
	slices.SortFunc(list, func(a, b ActiveSession) int { return a.Since.Compare(b.Since) })
	return list
}

func (s *Sessions) open(a ActiveSession) (func(), bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, busy := s.active[a.Node]; busy {
		return nil, false
	}
	s.active[a.Node] = a
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.active, a.Node)
	}, true
}

var (
	errNoIdentity       = errors.New("no agent identity")
	errHelloFirst       = errors.New("the first message must be a Hello")
	errDuplicateHello   = errors.New("hello may only be sent once per session")
	errDuplicateSession = errors.New("this node already has an active session")
)

// SessionHandler implements sermon.agent.v1.AgentService.
type SessionHandler struct {
	sessions *Sessions
	logger   *slog.Logger
}

var _ agentv1connect.AgentServiceHandler = (*SessionHandler)(nil)

// NewSessionHandler returns the AgentService handler.
func NewSessionHandler(sessions *Sessions, logger *slog.Logger) *SessionHandler {
	return &SessionHandler{sessions: sessions, logger: logger}
}

// Session runs the stream of a connected agent: Hello first, then heartbeats (ingest follows
// with M1-24).
func (h *SessionHandler) Session(ctx context.Context, stream *connect.BidiStream[agentv1.SessionRequest, agentv1.SessionResponse]) error {
	id, ok := IdentityFrom(ctx)
	if !ok {
		return connect.NewError(connect.CodeUnauthenticated, errNoIdentity)
	}
	first, err := stream.Receive()
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err //nolint:wrapcheck // connect errors must reach the client unchanged
	}
	hello := first.GetHello()
	if hello == nil {
		return connect.NewError(connect.CodeInvalidArgument, errHelloFirst)
	}
	release, ok := h.sessions.open(ActiveSession{
		Org: id.Org, Node: id.Node, AgentVersion: hello.GetAgentVersion(), Hostname: hello.GetHostFacts().GetHostname(), Since: time.Now(),
	})
	if !ok {
		// Possibly a stolen key used in parallel (KONZEPT §6): keep the existing session.
		h.logger.WarnContext(ctx, "security: second session for a node rejected", "org", id.Org.String(), "node", id.Node.String())
		return connect.NewError(connect.CodeAlreadyExists, errDuplicateSession)
	}
	defer release()
	h.logger.InfoContext(ctx, "agent connected", "org", id.Org.String(), "node", id.Node.String(), "agent_version", hello.GetAgentVersion())
	defer h.logger.InfoContext(ctx, "agent disconnected", "org", id.Org.String(), "node", id.Node.String())

	for {
		msg, err := stream.Receive()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err //nolint:wrapcheck // see above
		}
		if msg.GetHello() != nil {
			return connect.NewError(connect.CodeInvalidArgument, errDuplicateHello)
		}
		// Heartbeat: liveness and node status follow with M1-24.
	}
}
