//go:build integration

package webapi_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/pki"
	agentv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/agent/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/agent/v1/agentv1connect"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
)

// agentClient speaks gRPC like sermon-agent (tonic): HTTP/2, no Origin header, no cookies.
func agentClient(s stack) agentv1connect.EnrollmentServiceClient {
	return agentv1connect.NewEnrollmentServiceClient(s.server.Client(), s.server.URL, connect.WithGRPC())
}

func p256CSR(t *testing.T) []byte {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func enrollRequest(token string, csr []byte, machineID string) *connect.Request[agentv1.EnrollRequest] {
	return connect.NewRequest(&agentv1.EnrollRequest{
		Token: token, Csr: csr, AgentVersion: "0.0.0",
		HostFacts: &agentv1.HostFacts{
			Hostname: "web-01", MachineId: machineID, OsId: "debian", OsVersion: "13", KernelRelease: "6.12.0",
			Architecture: agentv1.Architecture_ARCHITECTURE_AMD64, CpuCount: 4, MemoryBytes: 8 << 30,
		},
	})
}

func newMachineID() string { return strings.ReplaceAll(uuid.NewString(), "-", "") }

func TestEnrollment_AgentGetsACertificateForTheTokensOrganization(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ann := login(t, s, "ann@example.org")
	ctx := context.Background()
	token, err := ann.enrollment.CreateEnrollmentToken(ctx, connect.NewRequest(&webv1.CreateEnrollmentTokenRequest{GroupId: groupAl, MaxUses: 2}))
	if err != nil {
		t.Fatal(err)
	}
	secret := token.Msg.GetSecret()
	agent := agentClient(s)
	machine := newMachineID()

	res, err := agent.Enroll(ctx, enrollRequest(secret, p256CSR(t), machine))
	if err != nil {
		t.Fatalf("Enroll() error = %v", err)
	}

	// The chain verifies against the returned trust anchor, for client authentication.
	cert, err := x509.ParseCertificate(res.Msg.GetCertificate())
	if err != nil {
		t.Fatal(err)
	}
	roots, intermediates := x509.NewCertPool(), x509.NewCertPool()
	root, _ := x509.ParseCertificate(res.Msg.GetTrustAnchor())
	roots.AddCert(root)
	for _, der := range res.Msg.GetIntermediates() {
		ic, _ := x509.ParseCertificate(der)
		intermediates.AddCert(ic)
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Errorf("certificate does not verify: %v", err)
	}
	org, node, err := pki.AgentIdentity(cert)
	if err != nil || org.String() != orgAlpha || node.String() != res.Msg.GetNodeId() {
		t.Errorf("identity = %v/%v (%v), want Alpha/%s", org, node, err, res.Msg.GetNodeId())
	}
	if usable, err := s.ca.CertificateUsable(ctx, strings.ToLower(cert.SerialNumber.Text(16))); err != nil || !usable {
		t.Errorf("certificate usable = %v, %v", usable, err)
	}
	if !res.Msg.GetNotAfter().AsTime().After(time.Now().Add(29 * 24 * time.Hour)) {
		t.Errorf("not after = %v, want about 30 days", res.Msg.GetNotAfter().AsTime())
	}

	// The same host again: conflict, the token is not used ...
	if _, err := agent.Enroll(ctx, enrollRequest(secret, p256CSR(t), machine)); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Errorf("same machine-id: %v, want ALREADY_EXISTS", err)
	}
	// ... so a second host can still use it, and then it is used up.
	if _, err := agent.Enroll(ctx, enrollRequest(secret, p256CSR(t), newMachineID())); err != nil {
		t.Errorf("second host: %v", err)
	}
	if _, err := agent.Enroll(ctx, enrollRequest(secret, p256CSR(t), newMachineID())); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("used-up token: %v, want UNAUTHENTICATED", err)
	}

	audit, _ := ann.access.ListAuditLog(ctx, connect.NewRequest(&webv1.ListAuditLogRequest{PageSize: 4}))
	if !strings.Contains(audit.Msg.String(), "node.enroll") || !strings.Contains(audit.Msg.String(), machine) {
		t.Errorf("audit lacks the enrollment: %v", audit.Msg)
	}
}

func TestEnrollment_RejectsBadTokensAndCSRs(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ann := login(t, s, "ann@example.org")
	ctx := context.Background()
	token, err := ann.enrollment.CreateEnrollmentToken(ctx, connect.NewRequest(&webv1.CreateEnrollmentTokenRequest{GroupId: groupAl, MaxUses: 10}))
	if err != nil {
		t.Fatal(err)
	}
	agent := agentClient(s)

	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	rsaCSR, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, rsaKey)
	tampered := p256CSR(t)
	tampered[len(tampered)-1] ^= 0xff

	cases := map[string]struct {
		req  *connect.Request[agentv1.EnrollRequest]
		want connect.Code
	}{
		"unknown token":   {enrollRequest("sme_"+strings.Repeat("A", 43), p256CSR(t), newMachineID()), connect.CodeUnauthenticated},
		"garbage token":   {enrollRequest("not-a-token", p256CSR(t), newMachineID()), connect.CodeUnauthenticated},
		"rsa key":         {enrollRequest(token.Msg.GetSecret(), rsaCSR, newMachineID()), connect.CodeInvalidArgument},
		"tampered csr":    {enrollRequest(token.Msg.GetSecret(), tampered, newMachineID()), connect.CodeInvalidArgument},
		"invalid machine": {enrollRequest(token.Msg.GetSecret(), p256CSR(t), "nope"), connect.CodeInvalidArgument},
	}
	for name, tc := range cases {
		if _, err := agent.Enroll(ctx, tc.req); connect.CodeOf(err) != tc.want {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
	// Rejected requests do not use the token.
	list, _ := ann.enrollment.ListEnrollmentTokens(ctx, connect.NewRequest(&webv1.ListEnrollmentTokensRequest{}))
	for _, tok := range list.Msg.GetTokens() {
		if tok.GetId() == token.Msg.GetToken().GetId() && tok.GetUses() != 0 {
			t.Errorf("token uses = %d after rejected requests, want 0", tok.GetUses())
		}
	}
}

func TestEnrollment_RevokedTokensStopWorking(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ann := login(t, s, "ann@example.org")
	ctx := context.Background()
	token, _ := ann.enrollment.CreateEnrollmentToken(ctx, connect.NewRequest(&webv1.CreateEnrollmentTokenRequest{GroupId: groupAl}))
	if _, err := ann.enrollment.RevokeEnrollmentToken(ctx, connect.NewRequest(&webv1.RevokeEnrollmentTokenRequest{TokenId: token.Msg.GetToken().GetId()})); err != nil {
		t.Fatal(err)
	}
	if _, err := agentClient(s).Enroll(ctx, enrollRequest(token.Msg.GetSecret(), p256CSR(t), newMachineID())); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("revoked token: %v, want UNAUTHENTICATED", err)
	}
}
