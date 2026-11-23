//go:build integration

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	agentv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/agent/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/agent/v1/agentv1connect"
)

// runningServer is a sermon-server started with run() (web and agent listener on random ports).
type runningServer struct {
	web, agent string
	logs       *syncBuffer
	stop       func()
}

func startTestServer(t *testing.T, extra map[string]string) runningServer {
	t.Helper()
	keysFile := sharedKeysFile()
	if _, err := ensureKeyFile(keysFile); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"SERMON_HTTP_ADDR": "127.0.0.1:0", "SERMON_AGENT_ADDR": "127.0.0.1:0", "SERMON_AGENT_HOSTNAMES": "localhost"}
	for k, v := range extra {
		env[k] = v
	}
	src := dbSource(t, keysFile, env)
	ctx, cancel := context.WithCancel(context.Background())
	logs := &syncBuffer{}
	done := make(chan int, 1)
	go func() { done <- run(ctx, nil, src, logs, logs) }()
	s := runningServer{logs: logs}
	s.web = waitFor(t, logs, regexp.MustCompile(`msg=listening .*addr=(\S+)`))
	s.agent = waitFor(t, logs, regexp.MustCompile(`msg="agent API listening" .*addr=(\S+)`))
	s.stop = func() {
		cancel()
		select {
		case code := <-done:
			if code != exitOK {
				t.Errorf("server exit code %d\n%s", code, logs.String())
			}
		case <-time.After(20 * time.Second):
			t.Error("server did not stop")
		}
	}
	return s
}

func hostFacts() *agentv1.HostFacts {
	return &agentv1.HostFacts{
		Hostname: "web-01", MachineId: strings.ReplaceAll(uuid.NewString(), "-", ""), OsId: "debian", OsVersion: "13",
		KernelRelease: "6.12.0", Architecture: agentv1.Architecture_ARCHITECTURE_ARM64, CpuCount: 2, MemoryBytes: 1 << 30,
	}
}

// testAgent is an enrolled agent: its key/certificate and the roots it trusts.
type testAgent struct {
	nodeID string
	cert   tls.Certificate
	roots  [][]byte // DER
}

func newKeyAndCSR(t *testing.T) (*ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	return key, csr
}

// enroll enrolls over the web listener (gRPC over h2c, like behind Caddy).
func (s runningServer) enroll(t *testing.T, token string) testAgent {
	t.Helper()
	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	h2c := &http.Client{Transport: &http.Transport{Protocols: protocols}, Timeout: 30 * time.Second}
	key, csr := newKeyAndCSR(t)
	res, err := agentv1connect.NewEnrollmentServiceClient(h2c, "http://"+s.web, connect.WithGRPC()).Enroll(context.Background(),
		connect.NewRequest(&agentv1.EnrollRequest{Token: token, Csr: csr, AgentVersion: "0.0.0", HostFacts: hostFacts()}))
	if err != nil {
		t.Fatalf("Enroll() error = %v\n%s", err, s.logs.String())
	}
	return testAgent{
		nodeID: res.Msg.GetNodeId(),
		cert:   tls.Certificate{Certificate: append([][]byte{res.Msg.GetCertificate()}, res.Msg.GetIntermediates()...), PrivateKey: key},
		roots:  res.Msg.GetTrustAnchors(),
	}
}

// sessionResult is what one session yielded.
type sessionResult struct {
	bundle  [][]byte         // trust bundle sent by the server
	rotated *tls.Certificate // new certificate if a rotation was requested
}

// session connects with cert, trusting roots, optionally rotates, and closes the session.
func (s runningServer) session(t *testing.T, cert tls.Certificate, roots [][]byte, rotate bool) (sessionResult, error) {
	t.Helper()
	pool := x509.NewCertPool()
	for _, der := range roots {
		c, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		pool.AddCert(c)
	}
	tr := &http.Transport{ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{
		RootCAs: pool, Certificates: []tls.Certificate{cert}, ServerName: "localhost", MinVersion: tls.VersionTLS13,
	}}
	client := agentv1connect.NewAgentServiceClient(&http.Client{Transport: tr, Timeout: 30 * time.Second}, "https://"+s.agent, connect.WithGRPC())
	stream := client.Session(context.Background())
	defer func() { _ = stream.CloseResponse() }()
	var result sessionResult
	hello := &agentv1.SessionRequest{Payload: &agentv1.SessionRequest_Hello{Hello: &agentv1.Hello{
		ProtocolVersion: 1, AgentVersion: "0.0.0", HostFacts: hostFacts(),
	}}}
	if err := stream.Send(hello); err != nil && !errors.Is(err, io.EOF) {
		return result, err
	}
	first, err := stream.Receive()
	if err != nil {
		return result, err
	}
	result.bundle = first.GetTrustBundle().GetRoots()
	if rotate {
		key, csr := newKeyAndCSR(t)
		if err := stream.Send(&agentv1.SessionRequest{Payload: &agentv1.SessionRequest_RotateCertificate{
			RotateCertificate: &agentv1.RotateCertificate{Csr: csr},
		}}); err != nil && !errors.Is(err, io.EOF) {
			return result, err
		}
		res, err := receiveSkippingConfig(stream)
		if err != nil {
			return result, err
		}
		issued := res.GetCertificateIssued()
		result.rotated = &tls.Certificate{Certificate: append([][]byte{issued.GetCertificate()}, issued.GetIntermediates()...), PrivateKey: key}
		// The server ends the session after a rotation; the agent reconnects with the new one.
		_ = stream.CloseRequest()
		if _, err := receiveSkippingConfig(stream); connect.CodeOf(err) != connect.CodeAborted {
			return result, fmt.Errorf("after the rotation: %w, want the session to end (ABORTED)", err)
		}
		return result, nil
	}
	_ = stream.CloseRequest()
	if _, err := receiveSkippingConfig(stream); !errors.Is(err, io.EOF) {
		return result, err
	}
	return result, nil
}

// receiveSkippingConfig returns the next message that is no ConfigUpdate: the server sends the
// node's configuration after the trust bundle (ADR 0030).
func receiveSkippingConfig(stream *connect.BidiStreamForClient[agentv1.SessionRequest, agentv1.SessionResponse]) (*agentv1.SessionResponse, error) {
	for {
		res, err := stream.Receive()
		if err != nil || res.GetConfigUpdate() == nil {
			return res, err
		}
	}
}
