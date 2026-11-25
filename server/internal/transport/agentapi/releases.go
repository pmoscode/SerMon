package agentapi

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	agentv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/agent/v1"
	"github.com/pmoscode/SerMon/server/internal/transport/download"
)

// Releases are the signed agent binaries a self-update fetches (ADR 0036).
type Releases interface {
	Find(version, arch string) (download.Release, bool)
	Open(rel download.Release) (io.ReadCloser, error)
}

// WithReleases serves FetchAgentRelease from releases; without it every release is NOT_FOUND.
func WithReleases(releases Releases) HandlerOption {
	return func(h *SessionHandler) { h.releases = releases }
}

// Limits of FetchAgentRelease per node.
const (
	FetchesPerHour = 10
	chunkSize      = 256 << 10
)

var (
	errNoRelease    = errors.New("no such signed release")
	errFetchLimited = errors.New("one release at a time and 10 an hour per node")
)

// fetchLimits counts the fetches per node.
type fetchLimits struct {
	mu     sync.Mutex
	active map[identity.NodeID]bool
	recent map[identity.NodeID][]time.Time
}

func (l *fetchLimits) take(node identity.NodeID, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active == nil {
		l.active, l.recent = map[identity.NodeID]bool{}, map[identity.NodeID][]time.Time{}
	}
	var kept []time.Time
	for _, t := range l.recent[node] {
		if now.Sub(t) < time.Hour {
			kept = append(kept, t)
		}
	}
	if l.active[node] || len(kept) >= FetchesPerHour {
		l.recent[node] = kept
		return false
	}
	l.active[node], l.recent[node] = true, append(kept, now)
	return true
}

func (l *fetchLimits) done(node identity.NodeID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.active, node)
	if len(l.recent[node]) == 0 {
		delete(l.recent, node)
	}
}

var arches = map[agentv1.Architecture]string{agentv1.Architecture_ARCHITECTURE_AMD64: "amd64", agentv1.Architecture_ARCHITECTURE_ARM64: "arm64"}

// FetchAgentRelease streams a signed release: the header with size and signature, then the
// binary in chunks. Node and organization come from the certificate.
func (h *SessionHandler) FetchAgentRelease(ctx context.Context, req *connect.Request[agentv1.FetchAgentReleaseRequest], stream *connect.ServerStream[agentv1.FetchAgentReleaseResponse]) error {
	id, ok := IdentityFrom(ctx)
	if !ok {
		return connect.NewError(connect.CodeUnauthenticated, errNoIdentity)
	}
	if h.releases == nil {
		return connect.NewError(connect.CodeNotFound, errNoRelease)
	}
	rel, found := h.releases.Find(req.Msg.GetVersion(), arches[req.Msg.GetArchitecture()])
	if !found {
		return connect.NewError(connect.CodeNotFound, errNoRelease)
	}
	if !h.fetches.take(id.Node, time.Now()) {
		return connect.NewError(connect.CodeResourceExhausted, errFetchLimited)
	}
	defer h.fetches.done(id.Node)
	file, err := h.releases.Open(rel)
	if err != nil {
		h.logger.WarnContext(ctx, "open agent release failed", "version", rel.Version, "error", err)
		return connect.NewError(connect.CodeNotFound, errNoRelease)
	}
	defer func() { _ = file.Close() }()
	h.logger.InfoContext(ctx, "agent release fetched", "node", id.Node.String(), "version", rel.Version, "arch", rel.Arch)
	if err := stream.Send(&agentv1.FetchAgentReleaseResponse{Part: &agentv1.FetchAgentReleaseResponse_Header{Header: &agentv1.AgentReleaseHeader{
		Size: uint64(rel.Size), Signature: string(rel.Signature), //nolint:gosec // G115: at most 64 MiB
	}}}); err != nil {
		return err //nolint:wrapcheck // connect errors must reach the client unchanged
	}
	buf := make([]byte, chunkSize)
	var sent int64
	for sent < rel.Size {
		n, err := io.ReadFull(io.LimitReader(file, rel.Size-sent), buf[:min(chunkSize, rel.Size-sent)])
		if n > 0 {
			if err := stream.Send(&agentv1.FetchAgentReleaseResponse{Part: &agentv1.FetchAgentReleaseResponse_Chunk{Chunk: buf[:n]}}); err != nil {
				return err //nolint:wrapcheck // connect errors must reach the client unchanged
			}
			sent += int64(n)
		}
		if err != nil {
			// The file changed since its check: the helper would refuse it anyway.
			return connect.NewError(connect.CodeUnavailable, errNoRelease)
		}
	}
	return nil
}
