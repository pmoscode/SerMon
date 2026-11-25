package agentapi_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	agentv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/agent/v1"
	"github.com/pmoscode/SerMon/server/internal/transport/agentapi"
	"github.com/pmoscode/SerMon/server/internal/transport/download"
)

type fakeReleases struct{ binary []byte }

func (f fakeReleases) Find(version, arch string) (download.Release, bool) {
	if version != "1.1.0" || arch != "arm64" {
		return download.Release{}, false
	}
	return download.Release{Version: version, Arch: arch, Size: int64(len(f.binary)), Signature: []byte("sig")}, true
}

func (f fakeReleases) Open(download.Release) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(f.binary)), nil
}

func fetch(t *testing.T, client interface {
	FetchAgentRelease(context.Context, *connect.Request[agentv1.FetchAgentReleaseRequest]) (*connect.ServerStreamForClient[agentv1.FetchAgentReleaseResponse], error)
}, version string,
) ([]byte, string, error) {
	t.Helper()
	stream, err := client.FetchAgentRelease(context.Background(), connect.NewRequest(&agentv1.FetchAgentReleaseRequest{
		Version: version, Architecture: agentv1.Architecture_ARCHITECTURE_ARM64,
	}))
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = stream.Close() }()
	var binary []byte
	var signature string
	for stream.Receive() {
		switch p := stream.Msg().GetPart().(type) {
		case *agentv1.FetchAgentReleaseResponse_Header:
			signature = p.Header.GetSignature()
		case *agentv1.FetchAgentReleaseResponse_Chunk:
			binary = append(binary, p.Chunk...)
		}
	}
	return binary, signature, stream.Err()
}

// ADR 0036: a node fetches a known release in chunks with its signature; unknown versions are
// NOT_FOUND, and a node fetches at most 10 an hour.
func TestFetchAgentRelease_StreamsTheReleaseWithinTheLimits(t *testing.T) {
	binary := bytes.Repeat([]byte("0123456789"), 70_000) // 700 kB: three chunks
	s := startServerFull(t, newTestCA(t), 0, nil, []agentapi.HandlerOption{agentapi.WithReleases(fakeReleases{binary: binary})})
	cert, _ := s.ca.agentCert(t, orgX, nodeX)
	client := s.client(t, &cert, tls.VersionTLS13)
	got, sig, err := fetch(t, client, "1.1.0")
	if err != nil || !bytes.Equal(got, binary) || sig != "sig" {
		t.Fatalf("fetch = %d bytes, %q, %v", len(got), sig, err)
	}
	if _, _, err := fetch(t, client, "9.9.9"); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("unknown version: %v", err)
	}
	for range agentapi.FetchesPerHour - 1 {
		if _, _, err := fetch(t, client, "1.1.0"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := fetch(t, client, "1.1.0"); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Errorf("the 11th fetch of the hour: %v", err)
	}
	// Another node has its own limit.
	other, _ := s.ca.agentCert(t, orgX, identity.NodeID(uuid.New()))
	if _, _, err := fetch(t, s.client(t, &other, tls.VersionTLS13), "1.1.0"); err != nil {
		t.Errorf("another node: %v", err)
	}
	// Without releases nothing is found.
	plain := startServerFull(t, newTestCA(t), 0, nil, nil)
	pc, _ := plain.ca.agentCert(t, orgX, nodeX)
	if _, _, err := fetch(t, plain.client(t, &pc, tls.VersionTLS13), "1.1.0"); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("without releases: %v", err)
	}
}
