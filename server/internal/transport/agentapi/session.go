package agentapi

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"sync"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/validate"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pmoscode/SerMon/server/internal/adapters/pki"
	"github.com/pmoscode/SerMon/server/internal/app/rotation"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	agentv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/agent/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/agent/v1/agentv1connect"
)

// Identity is the agent identity from the verified client certificate – the only source of
// organization and node for everything the agent sends (CLAUDE.md security guardrails).
type Identity struct {
	Org      tenant.OrgID
	Node     identity.NodeID
	Serial   string // of the client certificate of this connection
	RemoteIP string
}

func (id Identity) rotation() rotation.Identity {
	return rotation.Identity{Org: id.Org, Node: id.Node, Serial: id.Serial}
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
		leaf := r.TLS.PeerCertificates[0]
		org, node, err := pki.AgentIdentity(leaf)
		if err != nil {
			http.Error(w, "invalid agent identity", http.StatusUnauthorized)
			return
		}
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}
		id := Identity{Org: org, Node: node, Serial: hex.EncodeToString(leaf.SerialNumber.Bytes()), RemoteIP: ip}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, id)))
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
	Serial       string
	RemoteIP     string
	AgentVersion string
	Hostname     string
	Since        time.Time
}

type liveSession struct {
	ActiveSession
	end func(error) // ends the session with the given cause
}

// Sessions tracks connected agents; a node has at most one active session (KONZEPT §6).
type Sessions struct {
	mu     sync.Mutex
	active map[identity.NodeID]*liveSession
}

// NewSessions returns an empty registry.
func NewSessions() *Sessions { return &Sessions{active: map[identity.NodeID]*liveSession{}} }

// Active returns the connected agents.
func (s *Sessions) Active() []ActiveSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := make([]ActiveSession, 0, len(s.active))
	for _, a := range s.active {
		list = append(list, a.ActiveSession)
	}
	slices.SortFunc(list, func(a, b ActiveSession) int { return a.Since.Compare(b.Since) })
	return list
}

type claim int

const (
	claimed  claim = iota // no other session
	replaced              // the same agent reconnected: the old session ended
	conflict              // another holder of the identity: nothing registered
)

// claim registers a session. A second session with the same certificate from the same IP is a
// reconnect (the old connection is not dead yet) and replaces the old one; anything else is a
// conflict.
func (s *Sessions) claim(l *liveSession) (claim, *liveSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, busy := s.active[l.Node]
	if !busy {
		s.active[l.Node] = l
		return claimed, nil
	}
	if old.Serial == l.Serial && old.RemoteIP == l.RemoteIP {
		s.active[l.Node] = l
		old.end(errReplaced)
		return replaced, old
	}
	return conflict, old
}

func (s *Sessions) release(l *liveSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active[l.Node] == l {
		delete(s.active, l.Node)
	}
}

// terminate ends the node's session (e.g. after a lock).
func (s *Sessions) terminate(node identity.NodeID, cause error) {
	s.mu.Lock()
	l := s.active[node]
	delete(s.active, node)
	s.mu.Unlock()
	if l != nil {
		l.end(cause)
	}
}

var (
	errNoIdentity     = errors.New("no agent identity")
	errHelloFirst     = errors.New("the first message must be a Hello")
	errDuplicateHello = errors.New("hello may only be sent once per session")
	errLocked         = errors.New("this node already has an active session elsewhere; the identity was locked as a precaution")
	errReplaced       = errors.New("session replaced by a new connection of the same agent")
)

// Rotator renews certificates and locks identities (rotation.Service).
type Rotator interface {
	SessionOpened(ctx context.Context, id rotation.Identity, ip string) (string, error)
	Rotate(ctx context.Context, id rotation.Identity, csrDER []byte, ip, previousIP string) (rotation.Rotated, error)
	Lock(ctx context.Context, id rotation.Identity, reason string) error
}

// SessionHandler implements sermon.agent.v1.AgentService.
type SessionHandler struct {
	sessions *Sessions
	rotator  Rotator
	logger   *slog.Logger
}

var _ agentv1connect.AgentServiceHandler = (*SessionHandler)(nil)

// NewSessionHandler returns the AgentService handler.
func NewSessionHandler(sessions *Sessions, rotator Rotator, logger *slog.Logger) *SessionHandler {
	return &SessionHandler{sessions: sessions, rotator: rotator, logger: logger}
}

type received struct {
	msg *agentv1.SessionRequest
	err error
}

// Session runs the stream of a connected agent: Hello first, then heartbeats and certificate
// rotations (ingest follows with M1-24).
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

	ctx, end := context.WithCancelCause(ctx)
	defer end(nil)
	live := &liveSession{end: end, ActiveSession: ActiveSession{
		Org: id.Org, Node: id.Node, Serial: id.Serial, RemoteIP: id.RemoteIP,
		AgentVersion: hello.GetAgentVersion(), Hostname: hello.GetHostFacts().GetHostname(), Since: time.Now(),
	}}
	switch result, other := h.sessions.claim(live); result {
	case conflict:
		// Two holders of one identity (KONZEPT §6): lock it and end both sessions.
		h.logger.WarnContext(ctx, "security: second session for a node, identity locked",
			"org", id.Org.String(), "node", id.Node.String(), "ip", id.RemoteIP, "existing_ip", other.RemoteIP)
		if err := h.rotator.Lock(ctx, id.rotation(), "second session from "+id.RemoteIP+" while connected from "+other.RemoteIP); err != nil {
			h.logger.ErrorContext(ctx, "lock node failed", "node", id.Node.String(), "error", err)
		}
		h.sessions.terminate(id.Node, errLocked)
		return connect.NewError(connect.CodeAlreadyExists, errLocked)
	case replaced:
		h.logger.InfoContext(ctx, "agent reconnected, old session replaced", "node", id.Node.String())
	case claimed:
	}
	defer h.sessions.release(live)
	previousIP, err := h.rotator.SessionOpened(ctx, id.rotation(), id.RemoteIP)
	if err != nil {
		h.logger.ErrorContext(ctx, "record session failed", "node", id.Node.String(), "error", err)
	}
	h.logger.InfoContext(ctx, "agent connected", "org", id.Org.String(), "node", id.Node.String(), "agent_version", hello.GetAgentVersion())
	defer h.logger.InfoContext(ctx, "agent disconnected", "org", id.Org.String(), "node", id.Node.String())

	// Receive in the background, so that the session can be ended from outside (lock, replace).
	messages := make(chan received)
	go func() {
		for {
			msg, err := stream.Receive()
			select {
			case messages <- received{msg, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return connect.NewError(connect.CodeAborted, context.Cause(ctx))
		case r := <-messages:
			if errors.Is(r.err, io.EOF) {
				return nil
			}
			if r.err != nil {
				return r.err
			}
			if err := h.handle(ctx, id, previousIP, r.msg, stream); err != nil {
				return err
			}
		}
	}
}

func (h *SessionHandler) handle(ctx context.Context, id Identity, previousIP string, msg *agentv1.SessionRequest,
	stream *connect.BidiStream[agentv1.SessionRequest, agentv1.SessionResponse],
) error {
	switch {
	case msg.GetHello() != nil:
		return connect.NewError(connect.CodeInvalidArgument, errDuplicateHello)
	case msg.GetRotateCertificate() != nil:
		rotated, err := h.rotator.Rotate(ctx, id.rotation(), msg.GetRotateCertificate().GetCsr(), id.RemoteIP, previousIP)
		switch {
		case errors.Is(err, rotation.ErrLocked):
			h.logger.WarnContext(ctx, "security: rotation with a replaced certificate, identity locked", "node", id.Node.String(), "ip", id.RemoteIP)
			h.sessions.terminate(id.Node, rotation.ErrLocked)
			return connect.NewError(connect.CodePermissionDenied, rotation.ErrLocked)
		case errors.Is(err, rotation.ErrInvalidCSR):
			return connect.NewError(connect.CodeInvalidArgument, rotation.ErrInvalidCSR)
		case err != nil:
			h.logger.ErrorContext(ctx, "certificate rotation failed", "node", id.Node.String(), "error", err)
			return connect.NewError(connect.CodeInternal, errors.New("certificate rotation failed"))
		}
		h.logger.InfoContext(ctx, "agent certificate rotated", "node", id.Node.String())
		res := &agentv1.SessionResponse{Payload: &agentv1.SessionResponse_CertificateIssued{CertificateIssued: &agentv1.CertificateIssued{
			Certificate: rotated.Certificate, Intermediates: [][]byte{rotated.Intermediate},
			NotAfter: timestamppb.New(rotated.NotAfter), PreviousValidUntil: timestamppb.New(rotated.PreviousValidUntil),
		}}}
		return stream.Send(res) //nolint:wrapcheck // connect errors must reach the client unchanged
	}
	// Heartbeat: liveness and node status follow with M1-24.
	return nil
}
