// Package rotation renews agent certificates and protects identities against parallel use
// (KONZEPT §6 "Schlüsselrotation", M1-11).
package rotation

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"time"

	"github.com/pmoscode/SerMon/server/internal/adapters/pki"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/agentca"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Grace is how long a replaced certificate keeps working, so that the agent can reconnect with
// the new one.
const Grace = time.Hour

var (
	// ErrSuperseded means the session's certificate was already replaced or revoked (Store).
	ErrSuperseded = errors.New("certificate already replaced or revoked")
	// ErrLocked means the node was locked as a precaution; an admin must release or re-enroll it.
	ErrLocked = errors.New("node identity locked as a precaution; an admin must re-enroll the node")
	// ErrInvalidCSR means the rotation's CSR was rejected.
	ErrInvalidCSR = errors.New("invalid certificate signing request")
)

// Identity is a connected agent: organization and node from its client certificate, and the
// certificate's serial.
type Identity struct {
	Org    tenant.OrgID
	Node   identity.NodeID
	Serial string
}

// Rotated is the new certificate for the agent.
type Rotated struct {
	Certificate        []byte
	Intermediate       []byte
	NotAfter           time.Time
	PreviousValidUntil time.Time
}

// Signer signs agent certificates (agentca.Service).
type Signer interface {
	SignAgentCertificate(org tenant.OrgID, csrDER []byte, node identity.NodeID) (pki.Issued, agentca.CertificateRecord, error)
	Intermediate() *x509.Certificate
}

// Store persists rotations; every method runs in one transaction of the organization.
type Store interface {
	// Rotate schedules the revocation of id.Serial at revokeOldAt, records next and writes the
	// audit entry; ErrSuperseded if id.Serial was already replaced or revoked.
	Rotate(ctx context.Context, id Identity, next agentca.CertificateRecord, revokeOldAt time.Time, audit access.AuditEntry) error
	// Lock marks the node as locked and revokes all its certificates immediately.
	Lock(ctx context.Context, org tenant.OrgID, node identity.NodeID, reason string, now time.Time, audit access.AuditEntry) error
	// RecordSession stores the source IP of a new session and returns the previous one.
	RecordSession(ctx context.Context, org tenant.OrgID, node identity.NodeID, ip string) (string, error)
	// Note writes a security note to the organization's audit log.
	Note(ctx context.Context, org tenant.OrgID, audit access.AuditEntry) error
}

// Service rotates certificates.
type Service struct {
	store    Store
	signer   Signer
	now      func() time.Time
	onDemand *onDemand
}

// NewService returns a Service.
func NewService(store Store, signer Signer, now func() time.Time, opts ...Option) *Service {
	s := &Service{store: store, signer: signer, now: now}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// SessionOpened records the source IP of a new session and returns the previous session's IP.
func (s *Service) SessionOpened(ctx context.Context, id Identity, ip string) (string, error) {
	prev, err := s.store.RecordSession(ctx, id.Org, id.Node, ip)
	if err != nil {
		return "", fmt.Errorf("record session: %w", err)
	}
	return prev, nil
}

// Rotate issues a new certificate for the session's identity. The previous certificate stays
// valid for Grace – or not at all for an on-demand rotation (immediate). A rotation with an
// already replaced certificate means two parties hold the identity (stolen key or a lost race):
// the node is locked. A rotation from another source IP than the previous session leaves a
// security note.
func (s *Service) Rotate(ctx context.Context, id Identity, csrDER []byte, ip, previousIP string, immediate bool) (Rotated, error) {
	issued, record, err := s.signer.SignAgentCertificate(id.Org, csrDER, id.Node)
	if errors.Is(err, pki.ErrInvalidCSR) {
		return Rotated{}, ErrInvalidCSR
	}
	if err != nil {
		return Rotated{}, fmt.Errorf("sign rotated certificate: %w", err)
	}
	now := s.now()
	revokeOld := now.Add(Grace)
	if immediate {
		revokeOld = now
	}
	audit := access.AuditEntry{
		Action: "certificate.rotate", Target: "node:" + id.Node.String(),
		Details: map[string]string{"previous": id.Serial, "certificate": issued.Serial, "ip": ip},
	}
	err = s.store.Rotate(ctx, id, record, revokeOld, audit)
	if errors.Is(err, ErrSuperseded) {
		if lockErr := s.Lock(ctx, id, "rotation with an already replaced certificate from "+ip); lockErr != nil {
			return Rotated{}, lockErr
		}
		return Rotated{}, ErrLocked
	}
	if err != nil {
		return Rotated{}, fmt.Errorf("record rotation: %w", err)
	}
	if previousIP != "" && previousIP != ip {
		note := access.AuditEntry{
			Action: "security.rotation_new_ip", Target: "node:" + id.Node.String(),
			Details: map[string]string{"ip": ip, "previous_ip": previousIP},
		}
		if err := s.store.Note(ctx, id.Org, note); err != nil {
			return Rotated{}, fmt.Errorf("security note: %w", err)
		}
	}
	return Rotated{Certificate: issued.DER, Intermediate: s.signer.Intermediate().Raw, NotAfter: issued.NotAfter, PreviousValidUntil: revokeOld}, nil
}

// Lock locks the node as a precaution and revokes all its certificates (KONZEPT §6).
func (s *Service) Lock(ctx context.Context, id Identity, reason string) error {
	audit := access.AuditEntry{
		Action: "security.node_locked", Target: "node:" + id.Node.String(),
		Details: map[string]string{"reason": reason, "certificate": id.Serial},
	}
	if err := s.store.Lock(ctx, id.Org, id.Node, reason, s.now(), audit); err != nil {
		return fmt.Errorf("lock node: %w", err)
	}
	return nil
}
