package enrollment_test

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/pki"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/agentca"
	"github.com/pmoscode/SerMon/server/internal/app/enrollment"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

type fakeSigner struct {
	org  tenant.OrgID
	node identity.NodeID
	err  error
}

func (f *fakeSigner) SignAgentCertificate(org tenant.OrgID, _ []byte, node identity.NodeID) (pki.Issued, agentca.CertificateRecord, error) {
	f.org, f.node = org, node
	if f.err != nil {
		return pki.Issued{}, agentca.CertificateRecord{}, f.err
	}
	issued := pki.Issued{DER: []byte("cert"), Serial: "ab", NotAfter: now.Add(30 * 24 * time.Hour), Intermediate: []byte("intermediate"), Root: []byte("root")}
	return issued, agentca.CertificateRecord{Serial: "ab", Org: org, Node: node, NotAfter: issued.NotAfter}, nil
}

// TrustAnchor and Intermediate are the CA issuing *now*: after a rollover between signing and
// answering they are no longer the ones that signed (the enrollment must not use them, M1-38).
func (f *fakeSigner) TrustAnchor() *x509.Certificate {
	return &x509.Certificate{Raw: []byte("root-after-rollover")}
}

func (f *fakeSigner) TrustAnchors() []*x509.Certificate {
	return []*x509.Certificate{{Raw: []byte("old-root")}, {Raw: []byte("root")}}
}

func (f *fakeSigner) Intermediate() *x509.Certificate {
	return &x509.Certificate{Raw: []byte("intermediate-after-rollover")}
}

type fakeRegistry struct {
	node   enrollment.Node
	cert   agentca.CertificateRecord
	audits []access.AuditEntry
	err    error
	calls  int
	// takeover: the node whose takeover is allowed; takeovers counts the calls of TakeOver.
	takeover  identity.NodeID
	asked     string
	takeovers int
}

func (f *fakeRegistry) TakeoverCandidate(_ context.Context, st enrollment.Stored, machineID string, _ time.Time) (identity.NodeID, bool, error) {
	f.asked = st.Group.String() + "/" + machineID
	return f.takeover, !f.takeover.IsZero(), nil
}

func (f *fakeRegistry) TakeOver(_ context.Context, _ enrollment.Stored, n enrollment.Node, c agentca.CertificateRecord, _ time.Time, audits []access.AuditEntry) error {
	f.takeovers++
	f.node, f.cert, f.audits = n, c, audits
	return f.err
}

func (f *fakeRegistry) Register(_ context.Context, _ enrollment.Stored, n enrollment.Node, c agentca.CertificateRecord, _ time.Time, audits []access.AuditEntry) error {
	f.calls++
	f.node, f.cert, f.audits = n, c, audits
	return f.err
}

var secret = "sme_" + strings.Repeat("B", 43)

func facts() enrollment.NodeFacts {
	return enrollment.NodeFacts{
		Hostname: "web-01", MachineID: strings.Repeat("a", 32), OSID: "debian", OSVersion: "13",
		KernelRelease: "6.12.0", Architecture: "amd64", CPUCount: 4, MemoryBytes: 8 << 30,
	}
}

func enroller(store *fakeStore, signer *fakeSigner, registry *fakeRegistry) *enrollment.Enroller {
	return enrollment.NewEnroller(store, registry, signer, func() time.Time { return now }, uuid.New)
}

func validStore() *fakeStore {
	return &fakeStore{lookup: &enrollment.Stored{ID: enrollment.TokenID(uuid.New()), Org: orgA, Group: group1, Hash: sha256.Sum256([]byte(secret))}}
}

func TestEnroll_SignsForTheTokensOrganizationAndRegistersTheNode(t *testing.T) {
	signer, registry := &fakeSigner{}, &fakeRegistry{}
	res, err := enroller(validStore(), signer, registry).Enroll(context.Background(), secret, []byte("csr"), facts())
	if err != nil {
		t.Fatal(err)
	}
	if signer.org != orgA || signer.node.IsZero() || res.Node != signer.node || res.Org != orgA {
		t.Errorf("identity: signer %v/%v, result %+v", signer.org, signer.node, res)
	}
	if registry.node.ID != res.Node || registry.node.Group != group1 || registry.node.Facts.Hostname != "web-01" || registry.cert.Serial != "ab" {
		t.Errorf("registered %+v / %+v", registry.node, registry.cert)
	}
	// The chain of the CA that signed, not of the one issuing now (rollover in between, M1-38).
	if string(res.Certificate) != "cert" || string(res.Intermediate) != "intermediate" || string(res.TrustAnchor) != "root" {
		t.Errorf("result chain = %q %q %q", res.Certificate, res.Intermediate, res.TrustAnchor)
	}
	bundle := map[string]bool{}
	for _, root := range res.TrustBundle {
		bundle[string(root)] = true
	}
	if len(res.TrustBundle) != 2 || !bundle["old-root"] || !bundle["root"] {
		t.Errorf("trust bundle = %q, want both roots once", res.TrustBundle)
	}
	actions := fmt.Sprint(registry.audits[0].Action, ",", registry.audits[1].Action)
	if actions != "enrollment_token.use,node.enroll" || registry.audits[1].Details["machine_id"] != strings.Repeat("a", 32) {
		t.Errorf("audits = %+v", registry.audits)
	}
}

// Takeover (M2-23, T5): an operator allowed it for the node with this machine-id in the token's
// group; the certificate carries the existing node's ID, its history stays.
func TestEnroll_TakesOverAnAllowedNodeWithItsID(t *testing.T) {
	existing := identity.NodeID(uuid.New())
	signer, registry := &fakeSigner{}, &fakeRegistry{takeover: existing}
	res, err := enroller(validStore(), signer, registry).Enroll(context.Background(), secret, []byte("csr"), facts())
	if err != nil {
		t.Fatal(err)
	}
	if registry.asked != group1.String()+"/"+strings.Repeat("a", 32) {
		t.Errorf("asked for %q, want the token's group and the machine-id", registry.asked)
	}
	if signer.node != existing || res.Node != existing || registry.node.ID != existing || registry.node.Group != group1 {
		t.Errorf("signed for %v, result %v, registered %v; want the existing node", signer.node, res.Node, registry.node.ID)
	}
	if registry.takeovers != 1 || registry.calls != 0 {
		t.Errorf("TakeOver %d, Register %d; want the takeover only", registry.takeovers, registry.calls)
	}
	actions := fmt.Sprint(registry.audits[0].Action, ",", registry.audits[1].Action)
	if actions != "enrollment_token.use,node.takeover" || registry.audits[1].Target != "node:"+existing.String() {
		t.Errorf("audits = %+v", registry.audits)
	}
	// Without an allowed takeover a new node is registered (and the store reports a conflict).
	signer, registry = &fakeSigner{}, &fakeRegistry{}
	if _, err := enroller(validStore(), signer, registry).Enroll(context.Background(), secret, []byte("csr"), facts()); err != nil {
		t.Fatal(err)
	}
	if registry.calls != 1 || registry.takeovers != 0 || signer.node == existing {
		t.Errorf("Register %d, TakeOver %d", registry.calls, registry.takeovers)
	}
}

func TestEnroll_RejectsBadTokensAndCSRsBeforeRegistering(t *testing.T) {
	cases := map[string]struct {
		secret string
		store  *fakeStore
		signer *fakeSigner
		want   error
	}{
		"malformed token": {"sme_x", validStore(), &fakeSigner{}, enrollment.ErrInvalidToken},
		"unknown token":   {secret, &fakeStore{}, &fakeSigner{}, enrollment.ErrInvalidToken},
		"bad csr":         {secret, validStore(), &fakeSigner{err: fmt.Errorf("sign: %w", pki.ErrInvalidCSR)}, enrollment.ErrInvalidCSR},
	}
	for name, tc := range cases {
		registry := &fakeRegistry{}
		if _, err := enroller(tc.store, tc.signer, registry).Enroll(context.Background(), tc.secret, []byte("csr"), facts()); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
		if registry.calls != 0 {
			t.Errorf("%s: registry called", name)
		}
	}
}

func TestEnroll_PropagatesRegistryErrors(t *testing.T) {
	for _, want := range []error{enrollment.ErrInvalidToken, enrollment.ErrMachineIDConflict} {
		_, err := enroller(validStore(), &fakeSigner{}, &fakeRegistry{err: want}).Enroll(context.Background(), secret, []byte("csr"), facts())
		if !errors.Is(err, want) {
			t.Errorf("error = %v, want %v", err, want)
		}
	}
}
