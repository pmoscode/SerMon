package agentapi

import (
	"context"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"sync"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/validate"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pmoscode/SerMon/server/internal/adapters/pki"
	"github.com/pmoscode/SerMon/server/internal/app/ingest"
	"github.com/pmoscode/SerMon/server/internal/app/rotation"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/secevent"
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

// MaxMessageBytes bounds one message of the agent stream after decompression: a full batch (2 000
// samples with labels) stays far below it; it equals the default limit of the agent's tonic client.
const MaxMessageBytes = 4 << 20

// Mount registers the agent API on mux (served only on the mTLS listener).
func Mount(mux *http.ServeMux, sessions *SessionHandler) {
	path, handler := agentv1connect.NewAgentServiceHandler(sessions,
		connect.WithReadMaxBytes(MaxMessageBytes), connect.WithInterceptors(validate.NewInterceptor()))
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
	end      func(error)   // ends the session with the given cause
	commands chan struct{} // on-demand rotation requests for the stream

	mu        sync.Mutex
	pending   *time.Timer // on-demand rotation in progress; fires on timeout
	successor string      // serial issued by a rotation in this session
}

// rotatedTo records the serial of the certificate a rotation in this session issued.
func (l *liveSession) rotatedTo(serial string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.successor = serial
}

func (l *liveSession) isSuccessor(serial string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.successor != "" && l.successor == serial
}

// takePending reports whether an on-demand rotation was requested and clears it.
func (l *liveSession) takePending() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.pending == nil {
		return false
	}
	l.pending.Stop()
	l.pending = nil
	return true
}

// DefaultRotationTimeout is how long a connected agent has to answer an on-demand rotation.
const DefaultRotationTimeout = time.Minute

// Sessions tracks connected agents; a node has at most one active session (KONZEPT §6).
type Sessions struct {
	mu              sync.Mutex
	active          map[identity.NodeID]*liveSession
	rotationTimeout time.Duration
}

// NewSessions returns an empty registry; agents must answer on-demand rotations within
// rotationTimeout (0 = DefaultRotationTimeout).
func NewSessions(rotationTimeout time.Duration) *Sessions {
	if rotationTimeout == 0 {
		rotationTimeout = DefaultRotationTimeout
	}
	return &Sessions{active: map[identity.NodeID]*liveSession{}, rotationTimeout: rotationTimeout}
}

var _ rotation.Live = (*Sessions)(nil)

// RequestRotation asks the node's connected agent to rotate now (rotation.Live). onTimeout runs
// if the agent does not rotate within the timeout.
func (s *Sessions) RequestRotation(org tenant.OrgID, node identity.NodeID, onTimeout func()) bool {
	s.mu.Lock()
	l := s.active[node]
	s.mu.Unlock()
	if l == nil || l.Org != org {
		return false
	}
	l.mu.Lock()
	if l.pending == nil {
		l.pending = time.AfterFunc(s.rotationTimeout, func() {
			if l.takePending() {
				onTimeout()
			}
		})
	}
	l.mu.Unlock()
	select {
	case l.commands <- struct{}{}:
	default: // a request is already queued
	}
	return true
}

// Terminate ends the node's session with cause (rotation.Live).
func (s *Sessions) Terminate(node identity.NodeID, cause error) { s.terminate(node, cause) }

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
// reconnect (the old connection is not dead yet) and replaces the old one, as does the
// certificate a rotation in the old session issued (from any IP: it went to whoever held that
// session); anything else is a conflict.
func (s *Sessions) claim(l *liveSession) (claim, *liveSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, busy := s.active[l.Node]
	if !busy {
		s.active[l.Node] = l
		return claimed, nil
	}
	if (old.Serial == l.Serial && old.RemoteIP == l.RemoteIP) || old.isSuccessor(l.Serial) {
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
	errRotated        = errors.New("certificate rotated; reconnect with the new certificate")
)

// Rotator renews certificates and locks identities (rotation.Service).
type Rotator interface {
	SessionOpened(ctx context.Context, id rotation.Identity, ip string) (string, error)
	Rotate(ctx context.Context, id rotation.Identity, csrDER []byte, ip, previousIP string, immediate bool) (rotation.Rotated, error)
	Lock(ctx context.Context, id rotation.Identity, reason string) error
}

// TrustSource provides the trust bundle for agents and the state of their certificates
// (agentca.Service).
type TrustSource interface {
	TrustAnchors() []*x509.Certificate
	// CertificateUsable reports whether a serial is recorded for org and node, not revoked and
	// valid now.
	CertificateUsable(ctx context.Context, serial string, org tenant.OrgID, node identity.NodeID) (bool, error)
}

// certificateRecheck is how often an open session checks that its certificate is still usable:
// the handshake check alone would let a stream outlive a revocation or the certificate itself.
const certificateRecheck = time.Minute

var errCertificateUnusable = errors.New("the client certificate was revoked or expired, reconnect with a valid one")

// SessionHandler implements sermon.agent.v1.AgentService.
type SessionHandler struct {
	sessions *Sessions
	rotator  Rotator
	trust    TrustSource
	ingest   *ingest.Service
	recheck  time.Duration
	logger   *slog.Logger
}

// HandlerOption configures the SessionHandler.
type HandlerOption func(*SessionHandler)

// WithCertificateRecheck sets how often open sessions recheck their certificate (tests).
func WithCertificateRecheck(interval time.Duration) HandlerOption {
	return func(h *SessionHandler) { h.recheck = interval }
}

// WithIngest stores the agents' batches (M1-24); without it batches are accepted but neither
// stored nor acknowledged.
func WithIngest(service *ingest.Service) HandlerOption {
	return func(h *SessionHandler) { h.ingest = service }
}

var _ agentv1connect.AgentServiceHandler = (*SessionHandler)(nil)

// NewSessionHandler returns the AgentService handler.
func NewSessionHandler(sessions *Sessions, rotator Rotator, trust TrustSource, logger *slog.Logger, options ...HandlerOption) *SessionHandler {
	h := &SessionHandler{sessions: sessions, rotator: rotator, trust: trust, recheck: certificateRecheck, logger: logger}
	for _, option := range options {
		option(h)
	}
	return h
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
	live := &liveSession{end: end, commands: make(chan struct{}, 1), ActiveSession: ActiveSession{
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
	feed, flush, closeFeed, err := h.openIngest(ctx, id, hello)
	if err != nil {
		return err
	}
	defer closeFeed()
	h.logger.InfoContext(ctx, "agent connected", "org", id.Org.String(), "node", id.Node.String(), "agent_version", hello.GetAgentVersion())
	// The trust bundle first: during a CA rollover the agent learns the new root (KONZEPT §6).
	bundle := &agentv1.TrustBundle{}
	for _, root := range h.trust.TrustAnchors() {
		bundle.Roots = append(bundle.Roots, root.Raw)
	}
	if err := stream.Send(&agentv1.SessionResponse{Payload: &agentv1.SessionResponse_TrustBundle{TrustBundle: bundle}}); err != nil {
		return err //nolint:wrapcheck // connect errors must reach the client unchanged
	}
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
	recheck := time.NewTicker(h.recheck)
	defer recheck.Stop()
	for {
		select {
		case <-ctx.Done():
			return connect.NewError(connect.CodeAborted, context.Cause(ctx))
		case <-recheck.C:
			usable, err := h.trust.CertificateUsable(ctx, id.Serial, id.Org, id.Node)
			if err != nil {
				// A database hiccup must not disconnect every agent; the next check decides.
				h.logger.WarnContext(ctx, "certificate recheck failed", "node", id.Node.String(), "error", err)
				continue
			}
			if !usable {
				h.logger.WarnContext(ctx, "session ended: certificate revoked or expired", "node", id.Node.String(), "serial", id.Serial)
				return connect.NewError(connect.CodePermissionDenied, errCertificateUnusable)
			}
		case <-flush:
			if err := h.flush(ctx, feed, stream); err != nil {
				return err
			}
		case <-live.commands:
			h.logger.InfoContext(ctx, "on-demand rotation requested", "node", id.Node.String())
			if err := stream.Send(&agentv1.SessionResponse{Payload: &agentv1.SessionResponse_RotateNow{RotateNow: &agentv1.RotateNow{}}}); err != nil {
				return err //nolint:wrapcheck // see above
			}
		case r := <-messages:
			if errors.Is(r.err, io.EOF) {
				return nil
			}
			if r.err != nil {
				return r.err
			}
			done, err := h.handle(ctx, id, live, previousIP, r.msg, stream, feed)
			if err != nil {
				return err
			}
			if done {
				return connect.NewError(connect.CodeAborted, errRotated)
			}
		}
	}
}

// handle processes one agent message; done ends the session (after a rotation).
func (h *SessionHandler) handle(ctx context.Context, id Identity, live *liveSession, previousIP string, msg *agentv1.SessionRequest,
	stream *connect.BidiStream[agentv1.SessionRequest, agentv1.SessionResponse], feed *ingest.Session,
) (bool, error) {
	switch {
	case msg.GetHello() != nil:
		return false, connect.NewError(connect.CodeInvalidArgument, errDuplicateHello)
	case msg.GetRotateCertificate() != nil:
		immediate := live.takePending()
		rotated, err := h.rotator.Rotate(ctx, id.rotation(), msg.GetRotateCertificate().GetCsr(), id.RemoteIP, previousIP, immediate)
		switch {
		case errors.Is(err, rotation.ErrLocked):
			h.logger.WarnContext(ctx, "security: rotation with a replaced certificate, identity locked", "node", id.Node.String(), "ip", id.RemoteIP)
			h.sessions.terminate(id.Node, rotation.ErrLocked)
			return false, connect.NewError(connect.CodePermissionDenied, rotation.ErrLocked)
		case errors.Is(err, rotation.ErrInvalidCSR):
			return false, connect.NewError(connect.CodeInvalidArgument, rotation.ErrInvalidCSR)
		case err != nil:
			h.logger.ErrorContext(ctx, "certificate rotation failed", "node", id.Node.String(), "error", err)
			return false, connect.NewError(connect.CodeInternal, errors.New("certificate rotation failed"))
		}
		h.logger.InfoContext(ctx, "agent certificate rotated", "node", id.Node.String(), "on_demand", immediate)
		res := &agentv1.SessionResponse{Payload: &agentv1.SessionResponse_CertificateIssued{CertificateIssued: &agentv1.CertificateIssued{
			Certificate: rotated.Certificate, Intermediates: [][]byte{rotated.Intermediate},
			NotAfter: timestamppb.New(rotated.NotAfter), PreviousValidUntil: timestamppb.New(rotated.PreviousValidUntil),
		}}}
		if err := stream.Send(res); err != nil {
			return false, err //nolint:wrapcheck // connect errors must reach the client unchanged
		}
		// End the session after every rotation, so that the agent reconnects with the new
		// certificate at once (after an on-demand one the old is revoked already). The new serial
		// counts as successor if the reconnect overtakes the end of this session (M1-38).
		live.rotatedTo(rotated.Serial)
		return true, nil
	case msg.GetBatch() != nil:
		if feed == nil {
			// Without ingest nothing is persisted, so nothing is acknowledged: the agent keeps the
			// data in its buffer.
			h.logger.DebugContext(ctx, "batch received, ingest disabled", "node", id.Node.String(), "seq", msg.GetBatch().GetSeq())
			return false, nil
		}
		response := feed.Accept(ctx, toBatch(msg.GetBatch()))
		if response.Throttle != nil {
			throttle := &agentv1.Throttle{MinInterval: durationpb.New(*response.Throttle)}
			if err := stream.Send(&agentv1.SessionResponse{Payload: &agentv1.SessionResponse_Throttle{Throttle: throttle}}); err != nil {
				return false, err //nolint:wrapcheck // connect errors must reach the client unchanged
			}
		}
		if response.Ack != 0 {
			if err := sendAck(stream, response.Ack); err != nil {
				return false, err
			}
		}
		if feed.Due() {
			return false, h.flush(ctx, feed, stream)
		}
		return false, nil
	}
	// Heartbeat: liveness for the node status.
	if feed != nil {
		feed.Heartbeat(ctx)
	}
	return false, nil
}

// openIngest prepares the ingest of a connected node: the feed, the flush timer and the function
// to call when the session ends. Without ingest all three are inert.
func (h *SessionHandler) openIngest(ctx context.Context, id Identity, hello *agentv1.Hello) (*ingest.Session, <-chan time.Time, func(), error) {
	if h.ingest == nil {
		return nil, nil, func() {}, nil
	}
	feed, err := h.ingest.Open(ctx, id.Org, id.Node, factsFrom(hello.GetHostFacts()), hello.GetAgentVersion())
	if err != nil {
		h.logger.ErrorContext(ctx, "open ingest failed", "node", id.Node.String(), "error", err)
		return nil, nil, nil, connect.NewError(connect.CodeUnavailable, errors.New("ingest unavailable, retry later"))
	}
	ticker := time.NewTicker(h.ingest.Config().FlushInterval)
	closeFeed := func() {
		ticker.Stop()
		// The session context is gone; the final flush still gets a few seconds.
		final, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		feed.Close(final)
	}
	return feed, ticker.C, closeFeed, nil
}

// flush writes the complete windows and acknowledges what is stored. A failed write is logged and
// retried at the next tick; the agent keeps the data until then.
func (h *SessionHandler) flush(ctx context.Context, feed *ingest.Session, stream *connect.BidiStream[agentv1.SessionRequest, agentv1.SessionResponse]) error {
	ack, err := feed.Flush(ctx)
	if err != nil {
		h.logger.WarnContext(ctx, "metric write failed, will retry", "error", err)
		return nil
	}
	if ack == 0 {
		return nil
	}
	return sendAck(stream, ack)
}

func sendAck(stream *connect.BidiStream[agentv1.SessionRequest, agentv1.SessionResponse], seq uint64) error {
	return stream.Send(&agentv1.SessionResponse{Payload: &agentv1.SessionResponse_Ack{Ack: &agentv1.Ack{Seq: seq}}}) //nolint:wrapcheck // connect errors must reach the client unchanged
}

// toBatch converts a validated protocol batch. Identity is never taken from it.
func toBatch(b *agentv1.Batch) ingest.Batch {
	if events := b.GetSecurityEvents(); events != nil {
		batch := ingest.Batch{Seq: b.GetSeq(), Events: make([]secevent.Event, 0, len(events.GetEvents()))}
		for _, e := range events.GetEvents() {
			if event, ok := toEvent(e); ok {
				batch.Events = append(batch.Events, event)
			}
		}
		return batch
	}
	metrics := b.GetMetrics()
	batch := ingest.Batch{Seq: b.GetSeq(), CollectedAt: metrics.GetCollectedAt().AsTime(), Samples: make([]ingest.Sample, 0, len(metrics.GetSamples()))}
	for _, sample := range metrics.GetSamples() {
		value := float64(sample.GetIntValue())
		if v, ok := sample.GetValue().(*agentv1.Sample_DoubleValue); ok {
			value = v.DoubleValue
		}
		batch.Samples = append(batch.Samples, ingest.Sample{Metric: sample.GetMetric(), Labels: sample.GetLabels(), Value: value})
	}
	return batch
}

func factsFrom(f *agentv1.HostFacts) ingest.HostFacts {
	architecture := "amd64"
	if f.GetArchitecture() == agentv1.Architecture_ARCHITECTURE_ARM64 {
		architecture = "arm64"
	}
	return ingest.HostFacts{
		Hostname: f.GetHostname(), OSID: f.GetOsId(), OSVersion: f.GetOsVersion(), KernelRelease: f.GetKernelRelease(),
		Architecture: architecture, CPUCount: f.GetCpuCount(), MemoryBytes: f.GetMemoryBytes(),
	}
}

var methods = map[agentv1.SshAuthMethod]string{
	agentv1.SshAuthMethod_SSH_AUTH_METHOD_PASSWORD:             "password",
	agentv1.SshAuthMethod_SSH_AUTH_METHOD_PUBLICKEY:            "publickey",
	agentv1.SshAuthMethod_SSH_AUTH_METHOD_KEYBOARD_INTERACTIVE: "keyboard-interactive",
	agentv1.SshAuthMethod_SSH_AUTH_METHOD_HOSTBASED:            "hostbased",
	agentv1.SshAuthMethod_SSH_AUTH_METHOD_GSSAPI:               "gssapi",
	agentv1.SshAuthMethod_SSH_AUTH_METHOD_NONE:                 "none",
	agentv1.SshAuthMethod_SSH_AUTH_METHOD_OTHER:                "other",
}

func remote(r *agentv1.SshRemote) *secevent.Remote {
	out := &secevent.Remote{Host: r.GetHost(), Port: uint16(min(r.GetPort(), 65535))}
	if ip, err := netip.ParseAddr(r.GetIp()); err == nil {
		out.IP = ip
	}
	return out
}

// toEvent converts a validated protocol event; the node is set by the session. Event kinds of a
// newer agent are skipped.
func toEvent(e *agentv1.SecurityEvent) (secevent.Event, bool) {
	out := secevent.Event{OccurredAt: e.GetOccurredAt().AsTime()}
	d := &out.Details
	switch ev := e.GetEvent().(type) {
	case *agentv1.SecurityEvent_SshLoginFailed:
		m := ev.SshLoginFailed
		out.Kind, out.User, out.Remote = secevent.SSHLoginFailed, m.GetUser(), remote(m.GetRemote())
		d.Method, d.InvalidUser = methods[m.GetMethod()], m.GetInvalidUser()
	case *agentv1.SecurityEvent_SshInvalidUser:
		m := ev.SshInvalidUser
		out.Kind, out.User, out.Remote = secevent.SSHInvalidUser, m.GetUser(), remote(m.GetRemote())
	case *agentv1.SecurityEvent_SshLoginSucceeded:
		m := ev.SshLoginSucceeded
		out.Kind, out.User, out.Remote = secevent.SSHLoginSucceeded, m.GetUser(), remote(m.GetRemote())
		d.Method, d.KeyType, d.KeyFingerprint = methods[m.GetMethod()], m.GetKeyType(), m.GetKeyFingerprint()
	case *agentv1.SecurityEvent_SshPreauthDisconnect:
		m := ev.SshPreauthDisconnect
		out.Kind, out.User, out.Remote = secevent.SSHPreauthDisconnect, m.GetUser(), remote(m.GetRemote())
		d.InvalidUser = m.GetInvalidUser()
	case *agentv1.SecurityEvent_SudoAuthFailed:
		m := ev.SudoAuthFailed
		out.Kind, out.User, d.TTY = secevent.SudoAuthFailed, m.GetUser(), m.GetTty()
	case *agentv1.SecurityEvent_SudoNotAllowed:
		m := ev.SudoNotAllowed
		out.Kind, out.User, d.Target, d.TTY = secevent.SudoNotAllowed, m.GetUser(), m.GetTarget(), m.GetTty()
	case *agentv1.SecurityEvent_SudoCommand:
		m := ev.SudoCommand
		out.Kind, out.User, d.Target, d.TTY, d.Command = secevent.SudoCommand, m.GetUser(), m.GetTarget(), m.GetTty(), m.GetCommand()
	case *agentv1.SecurityEvent_AccountCreated:
		m := ev.AccountCreated
		out.Kind, out.User = secevent.AccountCreated, m.GetUser()
		if m.Uid != nil {
			uid := m.GetUid()
			d.UID = &uid
		}
	case *agentv1.SecurityEvent_AccountDeleted:
		out.Kind, out.User = secevent.AccountDeleted, ev.AccountDeleted.GetUser()
	case *agentv1.SecurityEvent_SystemReboot:
		m := ev.SystemReboot
		out.Kind, d.PreviousBootID = secevent.SystemReboot, m.GetPreviousBootId()
		if m.GetUptimeBefore() != nil {
			uptime := m.GetUptimeBefore().AsDuration()
			d.UptimeBefore = &uptime
		}
	case *agentv1.SecurityEvent_OomKill:
		m := ev.OomKill
		out.Kind, d.PID, d.Process = secevent.OOMKill, m.GetPid(), m.GetProcess()
	default:
		return secevent.Event{}, false
	}
	return out, true
}
