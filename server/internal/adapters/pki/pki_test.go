package pki_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/pki"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	now  = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	org  = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
	node = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000c1"))
)

func newCA(t *testing.T) *pki.Authority {
	t.Helper()
	ca, err := pki.NewAuthority(now)
	if err != nil {
		t.Fatalf("NewAuthority() error = %v", err)
	}
	return ca
}

func csr(t *testing.T, key any, template *x509.CertificateRequest) []byte {
	t.Helper()
	if template == nil {
		template = &x509.CertificateRequest{Subject: pkix.Name{CommonName: "web-01"}}
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, template, key)
	if err != nil {
		t.Fatalf("create CSR: %v", err)
	}
	return der
}

func p256(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func issue(t *testing.T, ca *pki.Authority) *x509.Certificate {
	t.Helper()
	issued, err := ca.SignAgentCSR(csr(t, p256(t), nil), org, node, now)
	if err != nil {
		t.Fatalf("SignAgentCSR() error = %v", err)
	}
	cert, err := x509.ParseCertificate(issued.DER)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func TestSignAgentCSR_IssuesAClientCertificateThatVerifiesAgainstTheRoot(t *testing.T) {
	ca := newCA(t)
	cert := issue(t, ca)

	roots := x509.NewCertPool()
	roots.AddCert(ca.Root())
	intermediates := x509.NewCertPool()
	intermediates.AddCert(ca.Intermediate())
	_, err := cert.Verify(x509.VerifyOptions{
		Roots: roots, Intermediates: intermediates, CurrentTime: now.Add(time.Hour),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	if err != nil {
		t.Fatalf("certificate does not verify: %v", err)
	}
}

// The chain comes with the certificate: read separately, a CA rollover in between could pair it
// with an intermediate that didn't sign it (M1-38).
func TestSignAgentCSR_ReturnsTheChainOfTheSigningCA(t *testing.T) {
	ca := newCA(t)
	issued, err := ca.SignAgentCSR(csr(t, p256(t), nil), org, node, now)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(issued.Intermediate, ca.Intermediate().Raw) || !bytes.Equal(issued.Root, ca.Root().Raw) {
		t.Error("Issued must carry the intermediate and root of the signing CA")
	}
}

func TestSignAgentCSR_CertificateProperties(t *testing.T) {
	cert := issue(t, newCA(t))

	if cert.IsCA || cert.BasicConstraintsValid && cert.IsCA {
		t.Error("agent certificate must not be a CA")
	}
	if len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		t.Errorf("ExtKeyUsage = %v, want only clientAuth", cert.ExtKeyUsage)
	}
	if cert.KeyUsage != x509.KeyUsageDigitalSignature {
		t.Errorf("KeyUsage = %v, want digitalSignature only", cert.KeyUsage)
	}
	if got := cert.NotAfter.Sub(now); got != pki.AgentCertificateLifetime {
		t.Errorf("lifetime = %s, want %s", got, pki.AgentCertificateLifetime)
	}
	if cert.NotBefore.After(now) {
		t.Error("NotBefore must not be in the future (clock skew tolerance)")
	}
	if cert.SerialNumber.Sign() <= 0 || cert.SerialNumber.BitLen() < 100 {
		t.Errorf("serial %s is not a large positive random number", cert.SerialNumber)
	}
	if len(cert.URIs) != 1 || cert.URIs[0].String() != "spiffe://sermon/org/"+org.String()+"/node/"+node.String() {
		t.Errorf("URIs = %v, want the SPIFFE identity", cert.URIs)
	}
	if len(cert.DNSNames)+len(cert.IPAddresses)+len(cert.EmailAddresses) != 0 {
		t.Error("agent certificate must not carry DNS, IP or e-mail names")
	}
}

func TestSignAgentCSR_IgnoresEverythingTheAgentAsksFor(t *testing.T) {
	evil, _ := url.Parse("spiffe://sermon/org/" + uuid.NewString() + "/node/" + uuid.NewString())
	template := &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: "admin"},
		DNSNames: []string{"sermon.example.com"},
		URIs:     []*url.URL{evil},
		ExtraExtensions: []pkix.Extension{{ // basicConstraints CA:TRUE
			Id: asn1.ObjectIdentifier{2, 5, 29, 19}, Critical: true, Value: []byte{0x30, 0x03, 0x01, 0x01, 0xff},
		}},
	}
	issued, err := newCA(t).SignAgentCSR(csr(t, p256(t), template), org, node, now)
	if err != nil {
		t.Fatalf("SignAgentCSR() error = %v", err)
	}
	cert, _ := x509.ParseCertificate(issued.DER)

	if cert.IsCA || len(cert.DNSNames) != 0 || len(cert.URIs) != 1 || cert.URIs[0].String() == evil.String() {
		t.Errorf("requested names/extensions must be ignored: CA=%v DNS=%v URIs=%v", cert.IsCA, cert.DNSNames, cert.URIs)
	}
	if cert.Subject.CommonName != node.String() {
		t.Errorf("CommonName = %q, want the node ID", cert.Subject.CommonName)
	}
}

func TestSignAgentCSR_RejectsUnsupportedKeys(t *testing.T) {
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	_, edKey, _ := ed25519.GenerateKey(rand.Reader)
	ca := newCA(t)

	for name, key := range map[string]any{"P-384": p384, "RSA": rsaKey, "Ed25519": edKey} {
		if _, err := ca.SignAgentCSR(csr(t, key, nil), org, node, now); !errors.Is(err, pki.ErrInvalidCSR) {
			t.Errorf("%s key: error = %v, want ErrInvalidCSR", name, err)
		}
	}
}

func TestSignAgentCSR_RejectsBrokenCSRs(t *testing.T) {
	ca := newCA(t)
	valid := csr(t, p256(t), nil)
	tampered := append([]byte(nil), valid...)
	tampered[len(tampered)-5] ^= 0xff // break the signature

	for name, der := range map[string][]byte{"garbage": []byte("not a csr"), "empty": nil, "bad signature": tampered} {
		if _, err := ca.SignAgentCSR(der, org, node, now); !errors.Is(err, pki.ErrInvalidCSR) {
			t.Errorf("%s: error = %v, want ErrInvalidCSR", name, err)
		}
	}
}

func TestSignAgentCSR_RejectsMissingIdentity(t *testing.T) {
	ca := newCA(t)
	if _, err := ca.SignAgentCSR(csr(t, p256(t), nil), tenant.OrgID{}, node, now); err == nil {
		t.Error("zero organization must be rejected")
	}
	if _, err := ca.SignAgentCSR(csr(t, p256(t), nil), org, identity.NodeID{}, now); err == nil {
		t.Error("zero node must be rejected")
	}
}

func TestSignAgentCSR_SerialsAreUnique(t *testing.T) {
	ca := newCA(t)
	seen := map[string]bool{}
	for range 50 {
		issued, err := ca.SignAgentCSR(csr(t, p256(t), nil), org, node, now)
		if err != nil {
			t.Fatal(err)
		}
		if seen[issued.Serial] {
			t.Fatalf("duplicate serial %s", issued.Serial)
		}
		seen[issued.Serial] = true
	}
}

func TestIntermediate_IsConstrainedToSermonIdentities(t *testing.T) {
	ca := newCA(t)
	inter := ca.Intermediate()

	if !inter.IsCA || !inter.MaxPathLenZero || inter.MaxPathLen != 0 {
		t.Error("intermediate must be a CA that cannot create further CAs")
	}
	if len(inter.PermittedURIDomains) != 1 || inter.PermittedURIDomains[0] != "sermon" {
		t.Errorf("PermittedURIDomains = %v, want [sermon]", inter.PermittedURIDomains)
	}
	if ca.Root().Subject.CommonName == inter.Subject.CommonName {
		t.Error("root and intermediate must have distinct names")
	}
}

func TestAgentIdentity_RoundTrip(t *testing.T) {
	cert := issue(t, newCA(t))

	gotOrg, gotNode, err := pki.AgentIdentity(cert)
	if err != nil || gotOrg != org || gotNode != node {
		t.Errorf("AgentIdentity() = %v, %v, %v; want %v, %v", gotOrg, gotNode, err, org, node)
	}
}

func TestAgentIdentity_RejectsAnythingButTheExactFormat(t *testing.T) {
	o, n := org.String(), node.String()
	bad := []string{
		"spiffe://sermon/org/" + o,                                                     // no node
		"spiffe://other/org/" + o + "/node/" + n,                                       // foreign trust domain
		"https://sermon/org/" + o + "/node/" + n,                                       // wrong scheme
		"spiffe://sermon:443/org/" + o + "/node/" + n,                                  // port
		"spiffe://user@sermon/org/" + o + "/node/" + n,                                 // userinfo
		"spiffe://sermon/org/" + o + "/node/" + n + "?x=1",                             // query
		"spiffe://sermon/org/" + o + "/node/" + n + "/extra",                           // extra segment
		"spiffe://sermon/org/" + "not-a-uuid" + "/node/" + n,                           // invalid org
		"spiffe://sermon/org/" + o + "/node/" + "00000000-0000-0000-0000-000000000000", // nil node
		"spiffe://sermon/ORG/" + o + "/node/" + n,                                      // case
	}
	for _, raw := range bad {
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		if _, _, err := pki.AgentIdentity(&x509.Certificate{URIs: []*url.URL{u}}); !errors.Is(err, pki.ErrInvalidIdentity) {
			t.Errorf("%s: error = %v, want ErrInvalidIdentity", raw, err)
		}
	}
	u1, _ := url.Parse("spiffe://sermon/org/" + o + "/node/" + n)
	u2, _ := url.Parse("spiffe://sermon/org/" + o + "/node/" + uuid.NewString())
	for name, cert := range map[string]*x509.Certificate{"no URI": {}, "two URIs": {URIs: []*url.URL{u1, u2}}} {
		if _, _, err := pki.AgentIdentity(cert); !errors.Is(err, pki.ErrInvalidIdentity) {
			t.Errorf("%s: error = %v, want ErrInvalidIdentity", name, err)
		}
	}
}

func TestAuthority_KeysCanBeExportedAndLoadedAgain(t *testing.T) {
	ca := newCA(t)
	exported, err := ca.Export()
	if err != nil {
		t.Fatalf("Export() error = %v", err)
	}

	loaded, err := pki.LoadAuthority(exported)
	if err != nil {
		t.Fatalf("LoadAuthority() error = %v", err)
	}
	cert := issue(t, loaded)
	if cert.Issuer.CommonName != ca.Intermediate().Subject.CommonName {
		t.Error("loaded authority must sign with the same intermediate")
	}
}

func TestLoadAuthority_RejectsMismatchedKeys(t *testing.T) {
	a, _ := newCA(t).Export()
	b, _ := newCA(t).Export()
	a.IntermediateKey = b.IntermediateKey

	if _, err := pki.LoadAuthority(a); err == nil {
		t.Error("intermediate key that does not match the certificate must be rejected")
	}
}

func TestLoadAuthority_RejectsBrokenMaterial(t *testing.T) {
	good, _ := newCA(t).Export()
	other, _ := newCA(t).Export()

	cases := map[string]func(e *pki.Exported){
		"garbage root certificate":         func(e *pki.Exported) { e.RootCert = []byte("x") },
		"garbage intermediate certificate": func(e *pki.Exported) { e.IntermediateCert = []byte("x") },
		"garbage root key":                 func(e *pki.Exported) { e.RootKey = []byte("x") },
		"root key of another CA":           func(e *pki.Exported) { e.RootKey = other.RootKey },
		"intermediate from another root": func(e *pki.Exported) {
			e.IntermediateCert, e.IntermediateKey = other.IntermediateCert, other.IntermediateKey
		},
		"non-ECDSA key": func(e *pki.Exported) {
			_, ed, _ := ed25519.GenerateKey(rand.Reader)
			e.IntermediateKey, _ = x509.MarshalPKCS8PrivateKey(ed)
		},
	}
	for name, mutate := range cases {
		e := good
		mutate(&e)
		if _, err := pki.LoadAuthority(e); !errors.Is(err, pki.ErrInvalidAuthority) {
			t.Errorf("%s: error = %v, want ErrInvalidAuthority", name, err)
		}
	}
}

func TestIssueServerCertificate_VerifiesForTheHostsAndOnlyForServerAuth(t *testing.T) {
	ca := newCA(t)
	cert, err := ca.IssueServerCertificate([]string{"agents.example.org", "192.0.2.10"}, now)
	if err != nil {
		t.Fatalf("IssueServerCertificate() error = %v", err)
	}
	leaf := cert.Leaf
	roots, inters := x509.NewCertPool(), x509.NewCertPool()
	roots.AddCert(ca.Root())
	inters.AddCert(ca.Intermediate())
	for _, host := range []string{"agents.example.org", "192.0.2.10"} {
		if _, err := leaf.Verify(x509.VerifyOptions{
			DNSName: host, Roots: roots, Intermediates: inters, CurrentTime: now,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}); err != nil {
			t.Errorf("verify for %s: %v", host, err)
		}
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: "other.example.org", Roots: roots, Intermediates: inters, CurrentTime: now}); err == nil {
		t.Error("certificate must not be valid for other hosts")
	}
	if len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth || len(leaf.URIs) != 0 {
		t.Errorf("EKU %v, URIs %v: want server auth only and no agent identity", leaf.ExtKeyUsage, leaf.URIs)
	}
	if got := leaf.NotAfter.Sub(now); got > pki.ServerCertificateLifetime || got < pki.ServerCertificateLifetime-time.Hour {
		t.Errorf("lifetime = %v, want about %v", got, pki.ServerCertificateLifetime)
	}
	if len(cert.Certificate) != 2 {
		t.Errorf("chain has %d certificates, want leaf and intermediate", len(cert.Certificate))
	}
	if _, err := ca.IssueServerCertificate(nil, now); err == nil {
		t.Error("a server certificate needs at least one host")
	}
}
