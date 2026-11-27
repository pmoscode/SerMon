// Package pki is the internal agent certificate authority (KONZEPT §6, §18.2).
//
// A root CA (offline use only: signs intermediates) and an intermediate CA, which is name
// constrained to SPIFFE identities "spiffe://sermon/…", issue short-lived client certificates
// for agents. The agent identity (organization and node) is encoded only in the URI SAN and is
// the single source of truth for the agent API (identity never comes from message payloads).
package pki

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Lifetimes (KONZEPT §6).
const (
	AgentCertificateLifetime = 30 * 24 * time.Hour
	// ServerCertificateLifetime is the lifetime of the agent API's server certificate; it is
	// renewed automatically in memory (KONZEPT §6 "Rotation auf Server-Seite").
	ServerCertificateLifetime = 7 * 24 * time.Hour
	// IntermediateLifetime: the issuing CA; nothing renews it, a rollover replaces root and
	// intermediate before it ends (V1-19).
	IntermediateLifetime = 2 * 365 * 24 * time.Hour
	RootLifetime         = 10 * 365 * 24 * time.Hour
	// clockSkew backdates NotBefore so agents with a slightly late clock accept new certificates.
	clockSkew   = 5 * time.Minute
	serialBytes = 16
	trustDomain = "sermon"
)

var (
	// ErrInvalidCSR means a certificate signing request was rejected.
	ErrInvalidCSR = errors.New("invalid certificate signing request")
	// ErrInvalidIdentity means a certificate does not carry exactly one valid agent identity.
	ErrInvalidIdentity = errors.New("invalid agent identity")
	// ErrInvalidAuthority means exported CA material is inconsistent.
	ErrInvalidAuthority = errors.New("invalid certificate authority material")
)

// Authority holds the root and the intermediate CA.
type Authority struct {
	root     *x509.Certificate
	rootKey  *ecdsa.PrivateKey
	inter    *x509.Certificate
	interKey *ecdsa.PrivateKey
}

// Issued is a newly issued agent certificate.
type Issued struct {
	DER       []byte
	Serial    string // lowercase hex
	NotBefore time.Time
	NotAfter  time.Time
	// Intermediate and Root are the chain of the authority that signed (DER): reading them from
	// the issuing CA later could pair the certificate with another one after a rollover.
	Intermediate, Root []byte
}

// Exported is the DER material of an authority; the keys must be stored encrypted (secrets.Box).
type Exported struct {
	RootCert         []byte
	RootKey          []byte // PKCS#8
	IntermediateCert []byte
	IntermediateKey  []byte // PKCS#8
}

// NewAuthority creates a new root and intermediate CA.
func NewAuthority(now time.Time) (*Authority, error) {
	rootKey, err := newKey()
	if err != nil {
		return nil, err
	}
	rootSerial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	rootTemplate := &x509.Certificate{
		SerialNumber:          rootSerial,
		Subject:               pkix.Name{Organization: []string{"SerMon"}, CommonName: "SerMon Agent Root CA"},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              now.Add(RootLifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}
	root, err := createCertificate(rootTemplate, rootTemplate, &rootKey.PublicKey, rootKey)
	if err != nil {
		return nil, err
	}

	interKey, err := newKey()
	if err != nil {
		return nil, err
	}
	interSerial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	inter, err := createCertificate(&x509.Certificate{
		SerialNumber:          interSerial,
		Subject:               pkix.Name{Organization: []string{"SerMon"}, CommonName: "SerMon Agent Issuing CA"},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              now.Add(IntermediateLifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
		// Even a stolen intermediate key can only issue SerMon agent identities.
		PermittedURIDomains: []string{trustDomain},
	}, root, &interKey.PublicKey, rootKey)
	if err != nil {
		return nil, err
	}
	return &Authority{root: root, rootKey: rootKey, inter: inter, interKey: interKey}, nil
}

// Expires is when the CA stops working: the earlier end of its root and intermediate certificate.
func (a *Authority) Expires() time.Time { return earlier(a.root.NotAfter, a.inter.NotAfter) }

// ExpiresAt is Expires of a stored CA, from its certificates (DER).
func ExpiresAt(rootDER, intermediateDER []byte) (time.Time, error) {
	root, err := x509.ParseCertificate(rootDER)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse root certificate: %w", err)
	}
	inter, err := x509.ParseCertificate(intermediateDER)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse intermediate certificate: %w", err)
	}
	return earlier(root.NotAfter, inter.NotAfter), nil
}

func earlier(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

// Root returns the root certificate (trust anchor for agents and the agent API).
func (a *Authority) Root() *x509.Certificate { return a.root }

// Intermediate returns the issuing certificate.
func (a *Authority) Intermediate() *x509.Certificate { return a.inter }

// SignAgentCSR issues a client certificate for the node. Everything the agent requests
// (subject, names, extensions) is ignored – only its P-256 public key is used.
func (a *Authority) SignAgentCSR(csrDER []byte, org tenant.OrgID, node identity.NodeID, now time.Time) (Issued, error) {
	if uuid.UUID(org) == uuid.Nil || node.IsZero() {
		return Issued{}, fmt.Errorf("%w: organization and node are required", ErrInvalidIdentity)
	}
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return Issued{}, fmt.Errorf("%w: %w", ErrInvalidCSR, err)
	}
	if err := csr.CheckSignature(); err != nil {
		return Issued{}, fmt.Errorf("%w: bad signature", ErrInvalidCSR)
	}
	pub, ok := csr.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return Issued{}, fmt.Errorf("%w: only ECDSA P-256 keys are accepted", ErrInvalidCSR)
	}

	serial, err := randomSerial()
	if err != nil {
		return Issued{}, err
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{Organization: []string{"SerMon"}, CommonName: node.String()},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              now.Add(AgentCertificateLifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
		URIs:                  []*url.URL{agentURI(org, node)},
	}
	cert, err := createCertificate(template, a.inter, pub, a.interKey)
	if err != nil {
		return Issued{}, err
	}
	return Issued{
		DER: cert.Raw, Serial: hex.EncodeToString(serial.Bytes()), NotBefore: cert.NotBefore, NotAfter: cert.NotAfter,
		Intermediate: a.Intermediate().Raw, Root: a.Root().Raw,
	}, nil
}

// IssueServerCertificate issues a TLS server certificate for the agent API with a fresh P-256
// key. hosts are DNS names or IP addresses the agents connect to. The certificate carries no
// agent identity (no URI SAN) and is only valid for server authentication.
func (a *Authority) IssueServerCertificate(hosts []string, now time.Time) (tls.Certificate, error) {
	if len(hosts) == 0 {
		return tls.Certificate{}, errors.New("server certificate needs at least one host")
	}
	key, err := newKey()
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := randomSerial()
	if err != nil {
		return tls.Certificate{}, err
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{Organization: []string{"SerMon"}, CommonName: "SerMon Agent API"},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              now.Add(ServerCertificateLifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, h)
		}
	}
	cert, err := createCertificate(template, a.inter, &key.PublicKey, a.interKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{cert.Raw, a.inter.Raw}, PrivateKey: key, Leaf: cert}, nil
}

// AgentIdentity extracts organization and node from a verified agent certificate. It accepts
// exactly one URI SAN in the canonical form spiffe://sermon/org/<uuid>/node/<uuid>.
func AgentIdentity(cert *x509.Certificate) (tenant.OrgID, identity.NodeID, error) {
	if len(cert.URIs) != 1 {
		return tenant.OrgID{}, identity.NodeID{}, fmt.Errorf("%w: want exactly one URI SAN, got %d", ErrInvalidIdentity, len(cert.URIs))
	}
	u := cert.URIs[0]
	parts := strings.Split(u.Path, "/")
	if u.Scheme != "spiffe" || u.Host != trustDomain || len(parts) != 5 || parts[0] != "" || parts[1] != "org" || parts[3] != "node" {
		return tenant.OrgID{}, identity.NodeID{}, ErrInvalidIdentity
	}
	org, errOrg := uuid.Parse(parts[2])
	node, errNode := uuid.Parse(parts[4])
	if errOrg != nil || errNode != nil || org == uuid.Nil || node == uuid.Nil {
		return tenant.OrgID{}, identity.NodeID{}, ErrInvalidIdentity
	}
	// Reject every non-canonical variant (userinfo, port, query, fragment, case, encodings).
	orgID, nodeID := tenant.OrgID(org), identity.NodeID(node)
	if u.String() != agentURI(orgID, nodeID).String() {
		return tenant.OrgID{}, identity.NodeID{}, ErrInvalidIdentity
	}
	return orgID, nodeID, nil
}

// Export returns the authority's certificates and keys as DER.
func (a *Authority) Export() (Exported, error) {
	rootKey, err := x509.MarshalPKCS8PrivateKey(a.rootKey)
	if err != nil {
		return Exported{}, fmt.Errorf("marshal root key: %w", err)
	}
	interKey, err := x509.MarshalPKCS8PrivateKey(a.interKey)
	if err != nil {
		return Exported{}, fmt.Errorf("marshal intermediate key: %w", err)
	}
	return Exported{RootCert: a.root.Raw, RootKey: rootKey, IntermediateCert: a.inter.Raw, IntermediateKey: interKey}, nil
}

// LoadAuthority restores an authority and verifies that keys and certificates belong together.
func LoadAuthority(e Exported) (*Authority, error) {
	root, err := x509.ParseCertificate(e.RootCert)
	if err != nil {
		return nil, fmt.Errorf("%w: root certificate: %w", ErrInvalidAuthority, err)
	}
	inter, err := x509.ParseCertificate(e.IntermediateCert)
	if err != nil {
		return nil, fmt.Errorf("%w: intermediate certificate: %w", ErrInvalidAuthority, err)
	}
	rootKey, err := parseKey(e.RootKey, root)
	if err != nil {
		return nil, fmt.Errorf("%w: root key: %w", ErrInvalidAuthority, err)
	}
	interKey, err := parseKey(e.IntermediateKey, inter)
	if err != nil {
		return nil, fmt.Errorf("%w: intermediate key: %w", ErrInvalidAuthority, err)
	}
	if err := inter.CheckSignatureFrom(root); err != nil {
		return nil, fmt.Errorf("%w: intermediate not signed by root: %w", ErrInvalidAuthority, err)
	}
	return &Authority{root: root, rootKey: rootKey, inter: inter, interKey: interKey}, nil
}

func agentURI(org tenant.OrgID, node identity.NodeID) *url.URL {
	return &url.URL{Scheme: "spiffe", Host: trustDomain, Path: "/org/" + org.String() + "/node/" + node.String()}
}

func parseKey(der []byte, cert *x509.Certificate) (*ecdsa.PrivateKey, error) {
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("parse key: %w", err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("not an ECDSA key")
	}
	if !key.PublicKey.Equal(cert.PublicKey) {
		return nil, errors.New("key does not match certificate")
	}
	return key, nil
}

func newKey() (*ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	return key, nil
}

func randomSerial() (*big.Int, error) {
	b := make([]byte, serialBytes)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("generate serial: %w", err)
	}
	// 127-bit positive number with the top bit set: always 16 bytes in DER, never negative.
	b[0] = (b[0] & 0x7f) | 0x40
	return new(big.Int).SetBytes(b), nil
}

func createCertificate(template, parent *x509.Certificate, pub crypto.PublicKey, signer crypto.Signer) (*x509.Certificate, error) {
	der, err := x509.CreateCertificate(rand.Reader, template, parent, pub, signer)
	if err != nil {
		return nil, fmt.Errorf("create certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse created certificate: %w", err)
	}
	return cert, nil
}
