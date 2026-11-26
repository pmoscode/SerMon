package agentapi

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
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

// Limits of FetchAgentRelease: per node, and at once per organization and on the instance
// (M4-31) – a release is some MiB the server reads and sends.
const (
	FetchesPerHour      = 10
	FetchesAtOncePerOrg = 20
	FetchesAtOnce       = 100
	chunkSize           = 256 << 10
)

var (
	errNoRelease    = errors.New("no such signed release")
	errFetchLimited = errors.New("one release at a time and 10 an hour per node, a few at once per organization and server")
)

// fetchLimits counts the fetches per node, organization and in all.
type fetchLimits struct {
	mu     sync.Mutex
	active map[identity.NodeID]bool
	orgs   map[tenant.OrgID]int
	total  int
	recent map[identity.NodeID][]time.Time
}

func (l *fetchLimits) take(org tenant.OrgID, node identity.NodeID, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active == nil {
		l.active, l.orgs, l.recent = map[identity.NodeID]bool{}, map[tenant.OrgID]int{}, map[identity.NodeID][]time.Time{}
	}
	// The hour's fetches only: a node that stopped fetching is forgotten.
	for n, times := range l.recent {
		kept := slices.DeleteFunc(times, func(t time.Time) bool { return now.Sub(t) >= time.Hour })
		if len(kept) == 0 {
			delete(l.recent, n)
		} else {
			l.recent[n] = kept
		}
	}
	if l.active[node] || len(l.recent[node]) >= FetchesPerHour || l.orgs[org] >= FetchesAtOncePerOrg || l.total >= FetchesAtOnce {
		return false
	}
	l.active[node], l.recent[node] = true, append(l.recent[node], now)
	l.orgs[org]++
	l.total++
	return true
}

func (l *fetchLimits) done(org tenant.OrgID, node identity.NodeID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.active, node)
	l.total--
	if l.orgs[org]--; l.orgs[org] <= 0 {
		delete(l.orgs, org)
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
	if !h.fetches.take(id.Org, id.Node, time.Now()) {
		return connect.NewError(connect.CodeResourceExhausted, errFetchLimited)
	}
	defer h.fetches.done(id.Org, id.Node)
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
