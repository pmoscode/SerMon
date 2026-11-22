package enrollment

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/pki"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/agentca"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	// ErrInvalidCSR means the certificate signing request was rejected (signature, key type).
	ErrInvalidCSR = errors.New("invalid certificate signing request")
	// ErrMachineIDConflict means the organization already has a node with this machine-id; a
	// takeover needs a confirmation in the UI (ADR 0009). The token is not used.
	ErrMachineIDConflict = errors.New("a node with this machine-id already exists; allow its takeover in the UI (node, Identity) and enroll with a token of its group")
)

// NodeFacts are the host facts reported at enrollment (validated by protovalidate).
type NodeFacts struct {
	Hostname      string
	MachineID     string
	OSID          string
	OSVersion     string
	KernelRelease string
	Architecture  string // "amd64" or "arm64"
	CPUCount      int
	MemoryBytes   int64
}

// Node is a node created by enrollment.
type Node struct {
	ID    identity.NodeID
	Group tenant.GroupID
	Token TokenID
	Facts NodeFacts
}

// Enrolled is the result for the agent.
type Enrolled struct {
	Org          tenant.OrgID
	Node         identity.NodeID
	Certificate  []byte   // DER
	Intermediate []byte   // DER, issuer of Certificate
	TrustAnchor  []byte   // DER, root of the issuing agent CA
	TrustBundle  [][]byte // DER, all roots to trust for the mTLS agent API (old and new during a rollover)
	NotAfter     time.Time
}

// Signer signs agent certificates (agentca.Service); the issued certificate carries the chain of
// the CA that signed it.
type Signer interface {
	SignAgentCertificate(org tenant.OrgID, csrDER []byte, node identity.NodeID) (pki.Issued, agentca.CertificateRecord, error)
	TrustAnchors() []*x509.Certificate
}

// Registry performs the enrollment transaction in the token's organization: use the token once
// (ErrInvalidToken if it became invalid), create the node (ErrMachineIDConflict), record the
// certificate and write the audit entries.
type Registry interface {
	Register(ctx context.Context, token Stored, node Node, cert agentca.CertificateRecord, now time.Time, audits []access.AuditEntry) error
	// TakeoverCandidate returns the node of the token's group with this machine-id whose takeover
	// an operator allowed until after now (M2-23).
	TakeoverCandidate(ctx context.Context, token Stored, machineID string, now time.Time) (identity.NodeID, bool, error)
	// TakeOver uses the token, revokes every certificate of the node, records the new one, takes
	// the new host facts and closes the takeover window and any lock – in one transaction, and only
	// while the window is still open (ErrMachineIDConflict otherwise).
	TakeOver(ctx context.Context, token Stored, node Node, cert agentca.CertificateRecord, now time.Time, audits []access.AuditEntry) error
}

// Enroller enrolls new agents.
type Enroller struct {
	tokens   Store
	registry Registry
	signer   Signer
	now      func() time.Time
	newID    func() uuid.UUID
}

// NewEnroller returns an Enroller.
func NewEnroller(tokens Store, registry Registry, signer Signer, now func() time.Time, newID func() uuid.UUID) *Enroller {
	return &Enroller{tokens: tokens, registry: registry, signer: signer, now: now, newID: newID}
}

// Enroll checks the token, signs the CSR for a new node of the token's organization and group
// and registers both. The organization and node identity come only from the token and the
// server – never from the request.
func (e *Enroller) Enroll(ctx context.Context, secret string, csrDER []byte, facts NodeFacts) (Enrolled, error) {
	stored, err := e.find(ctx, secret)
	if err != nil {
		return Enrolled{}, err
	}
	node := Node{ID: identity.NodeID(e.newID()), Group: stored.Group, Token: stored.ID, Facts: facts}
	// A takeover keeps the node's ID (and its history): the certificate must carry it.
	existing, takeover, err := e.registry.TakeoverCandidate(ctx, stored, facts.MachineID, e.now())
	if err != nil {
		return Enrolled{}, fmt.Errorf("look for a takeover: %w", err)
	}
	if takeover {
		node.ID = existing
	}
	// Signing before the transaction is safe: an unrecorded certificate is never usable (M1-07).
	issued, record, err := e.signer.SignAgentCertificate(stored.Org, csrDER, node.ID)
	if errors.Is(err, pki.ErrInvalidCSR) {
		return Enrolled{}, ErrInvalidCSR
	}
	if err != nil {
		return Enrolled{}, fmt.Errorf("sign agent certificate: %w", err)
	}
	audits := []access.AuditEntry{
		{
			Action: "enrollment_token.use", Target: "enrollment_token:" + stored.ID.String(),
			Details: map[string]string{"group": stored.Group.String(), "node": node.ID.String()},
		},
		{Action: "node.enroll", Target: "node:" + node.ID.String(), Details: map[string]string{
			"group": stored.Group.String(), "hostname": facts.Hostname, "machine_id": facts.MachineID,
			"certificate": issued.Serial, "cpus": strconv.Itoa(facts.CPUCount),
		}},
	}
	register := e.registry.Register
	if takeover {
		audits[1].Action = "node.takeover"
		register = e.registry.TakeOver
	}
	if err := register(ctx, stored, node, record, e.now(), audits); err != nil {
		return Enrolled{}, fmt.Errorf("register node: %w", err)
	}
	// The bundle always contains the signing root, even if the trusted CAs changed meanwhile.
	bundle := [][]byte{issued.Root}
	for _, root := range e.signer.TrustAnchors() {
		if !bytes.Equal(root.Raw, issued.Root) {
			bundle = append(bundle, root.Raw)
		}
	}
	return Enrolled{
		Org: stored.Org, Node: node.ID, Certificate: issued.DER, NotAfter: issued.NotAfter,
		Intermediate: issued.Intermediate, TrustAnchor: issued.Root, TrustBundle: bundle,
	}, nil
}

// find returns the usable token for secret (ErrInvalidToken otherwise).
func (e *Enroller) find(ctx context.Context, secret string) (Stored, error) {
	if len(secret) != tokenLength || !strings.HasPrefix(secret, prefix) {
		return Stored{}, ErrInvalidToken
	}
	hash := sha256.Sum256([]byte(secret))
	stored, found, err := e.tokens.Lookup(ctx, hash, e.now())
	if err != nil {
		return Stored{}, fmt.Errorf("look up enrollment token: %w", err)
	}
	// The lookup is by hash; the constant-time comparison is defense in depth.
	if !found || subtle.ConstantTimeCompare(stored.Hash[:], hash[:]) != 1 {
		return Stored{}, ErrInvalidToken
	}
	return stored, nil
}
