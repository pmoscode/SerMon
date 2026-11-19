// Package agentca runs the internal agent certificate authority (KONZEPT §6): it loads or creates
// the CA, issues and revokes agent certificates and answers the revocation check of the agent API.
package agentca

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/pmoscode/SerMon/server/internal/adapters/pki"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	"github.com/pmoscode/SerMon/server/internal/secrets"
)

var (
	// ErrAuthorityExists is returned by Store.InsertAuthority when another instance stored one first.
	ErrAuthorityExists = errors.New("an active certificate authority already exists")
	// ErrNotFound means the certificate does not exist in the tenant or is already revoked.
	ErrNotFound = errors.New("certificate not found or already revoked")
)

// AuthorityRecord is the persisted CA: certificates in DER, private keys sealed with the master key.
type AuthorityRecord struct {
	RootCert              []byte
	RootKeySealed         []byte
	IntermediateCert      []byte
	IntermediateKeySealed []byte
}

// CertificateRecord is an issued agent certificate.
type CertificateRecord struct {
	Serial    string
	Org       tenant.OrgID
	Node      identity.NodeID
	NotBefore time.Time
	NotAfter  time.Time
}

// Store persists CA material and issued certificates (implemented in adapters/postgres).
type Store interface {
	ActiveAuthority(ctx context.Context) (AuthorityRecord, bool, error)
	InsertAuthority(ctx context.Context, record AuthorityRecord) error
	RecordCertificate(ctx context.Context, scope tenant.Scope, record CertificateRecord) error
	Revoke(ctx context.Context, scope tenant.Scope, serial string, at time.Time) (bool, error)
	Usable(ctx context.Context, serial string, at time.Time) (bool, error)
}

// Service is the agent CA.
type Service struct {
	store Store
	ca    *pki.Authority
	now   func() time.Time
}

// Bootstrap loads the active authority or creates and stores a new one.
func Bootstrap(ctx context.Context, store Store, box *secrets.Box, now func() time.Time) (*Service, error) {
	ca, err := loadOrCreate(ctx, store, box, now)
	if err != nil {
		return nil, err
	}
	return &Service{store: store, ca: ca, now: now}, nil
}

func loadOrCreate(ctx context.Context, store Store, box *secrets.Box, now func() time.Time) (*pki.Authority, error) {
	record, found, err := store.ActiveAuthority(ctx)
	if err != nil {
		return nil, fmt.Errorf("load certificate authority: %w", err)
	}
	if found {
		return open(record, box)
	}

	ca, err := pki.NewAuthority(now())
	if err != nil {
		return nil, fmt.Errorf("create certificate authority: %w", err)
	}
	record, err = seal(ca, box)
	if err != nil {
		return nil, err
	}
	switch err := store.InsertAuthority(ctx, record); {
	case errors.Is(err, ErrAuthorityExists):
		// Another server instance won the race: use its authority.
		return loadOrCreate(ctx, store, box, now)
	case err != nil:
		return nil, fmt.Errorf("store certificate authority: %w", err)
	}
	return ca, nil
}

// TrustAnchor returns the root certificate agents and the agent API trust.
func (s *Service) TrustAnchor() *x509.Certificate { return s.ca.Root() }

// Intermediate returns the issuing CA certificate (sent along with agent certificates).
func (s *Service) Intermediate() *x509.Certificate { return s.ca.Intermediate() }

// IssueAgentCertificate signs the agent's CSR for node in the scope's organization and records it.
func (s *Service) IssueAgentCertificate(ctx context.Context, scope tenant.Scope, csrDER []byte, node identity.NodeID) (pki.Issued, error) {
	if !scope.Valid() {
		return pki.Issued{}, errors.New("issuing an agent certificate requires a tenant scope")
	}
	issued, record, err := s.SignAgentCertificate(scope.OrgID(), csrDER, node)
	if err != nil {
		return pki.Issued{}, err
	}
	if err := s.store.RecordCertificate(ctx, scope, record); err != nil {
		return pki.Issued{}, fmt.Errorf("record agent certificate: %w", err)
	}
	return issued, nil
}

// SignAgentCertificate signs the CSR without recording it. The caller must record the returned
// record in the same transaction that creates the node (enrollment); until then the certificate
// is unusable because the agent API accepts recorded serials only.
func (s *Service) SignAgentCertificate(org tenant.OrgID, csrDER []byte, node identity.NodeID) (pki.Issued, CertificateRecord, error) {
	issued, err := s.ca.SignAgentCSR(csrDER, org, node, s.now())
	if err != nil {
		return pki.Issued{}, CertificateRecord{}, fmt.Errorf("sign agent certificate: %w", err)
	}
	record := CertificateRecord{Serial: issued.Serial, Org: org, Node: node, NotBefore: issued.NotBefore, NotAfter: issued.NotAfter}
	return issued, record, nil
}

// IssueServerCertificate issues a fresh server certificate for the agent API (not recorded:
// it identifies the server, not an agent).
func (s *Service) IssueServerCertificate(hosts []string) (tls.Certificate, error) {
	cert, err := s.ca.IssueServerCertificate(hosts, s.now())
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("issue server certificate: %w", err)
	}
	return cert, nil
}

// Revoke makes the certificate unusable immediately.
func (s *Service) Revoke(ctx context.Context, scope tenant.Scope, serial string) error {
	revoked, err := s.store.Revoke(ctx, scope, serial, s.now())
	if err != nil {
		return fmt.Errorf("revoke agent certificate: %w", err)
	}
	if !revoked {
		return ErrNotFound
	}
	return nil
}

// CertificateUsable reports whether the certificate exists, is not revoked and is within its
// validity period (checked on every agent connection; unknown serials are unusable).
func (s *Service) CertificateUsable(ctx context.Context, serial string) (bool, error) {
	usable, err := s.store.Usable(ctx, serial, s.now())
	if err != nil {
		return false, fmt.Errorf("check agent certificate: %w", err)
	}
	return usable, nil
}

func seal(ca *pki.Authority, box *secrets.Box) (AuthorityRecord, error) {
	exported, err := ca.Export()
	if err != nil {
		return AuthorityRecord{}, fmt.Errorf("export certificate authority: %w", err)
	}
	rootKey, err := box.Seal(exported.RootKey, keyContext("root", exported.RootCert))
	if err != nil {
		return AuthorityRecord{}, fmt.Errorf("seal root key: %w", err)
	}
	interKey, err := box.Seal(exported.IntermediateKey, keyContext("intermediate", exported.IntermediateCert))
	if err != nil {
		return AuthorityRecord{}, fmt.Errorf("seal intermediate key: %w", err)
	}
	return AuthorityRecord{
		RootCert: exported.RootCert, RootKeySealed: rootKey,
		IntermediateCert: exported.IntermediateCert, IntermediateKeySealed: interKey,
	}, nil
}

func open(record AuthorityRecord, box *secrets.Box) (*pki.Authority, error) {
	rootKey, err := box.Open(record.RootKeySealed, keyContext("root", record.RootCert))
	if err != nil {
		return nil, fmt.Errorf("unseal root key: %w", err)
	}
	interKey, err := box.Open(record.IntermediateKeySealed, keyContext("intermediate", record.IntermediateCert))
	if err != nil {
		return nil, fmt.Errorf("unseal intermediate key: %w", err)
	}
	ca, err := pki.LoadAuthority(pki.Exported{
		RootCert: record.RootCert, RootKey: rootKey, IntermediateCert: record.IntermediateCert, IntermediateKey: interKey,
	})
	if err != nil {
		return nil, fmt.Errorf("load certificate authority: %w", err)
	}
	return ca, nil
}

// keyContext binds a sealed key to its certificate, so keys cannot be swapped between records.
func keyContext(kind string, certDER []byte) []byte {
	sum := sha256.Sum256(certDER)
	return []byte("pki:" + kind + ":" + hex.EncodeToString(sum[:]))
}
