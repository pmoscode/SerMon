// Package agentca runs the internal agent certificate authority (KONZEPT §6): it loads or creates
// the CA, issues and revokes agent certificates, answers the revocation check of the agent API
// and rolls the CA over (trust bundle with old and new root until the old CA is retired).
package agentca

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/pmoscode/SerMon/server/internal/adapters/pki"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	"github.com/pmoscode/SerMon/server/internal/secrets"
)

var (
	// ErrAuthorityExists is returned by Store.InsertAuthority when another instance stored one first.
	ErrAuthorityExists = errors.New("an active certificate authority already exists")
	// ErrNotFound means the certificate does not exist in the tenant or is already revoked.
	ErrNotFound = errors.New("certificate not found or already revoked")
	// ErrRolloverInProgress means a previous CA is still trusted; it must be retired first.
	ErrRolloverInProgress = errors.New("a CA rollover is in progress; retire the previous CA first")
	// ErrAuthorityInUse means the CA still has valid agent certificates.
	ErrAuthorityInUse = errors.New("the CA still has valid agent certificates")
	// ErrIssuingAuthority means the issuing CA cannot be retired.
	ErrIssuingAuthority = errors.New("the issuing CA cannot be retired")
	// ErrUnknownAuthority means the CA does not exist or is already retired.
	ErrUnknownAuthority = errors.New("unknown or already retired CA")
)

// AuthorityRecord is the persisted CA: certificates in DER, private keys sealed with the master key.
type AuthorityRecord struct {
	RootCert              []byte
	RootKeySealed         []byte
	IntermediateCert      []byte
	IntermediateKeySealed []byte
}

// StoredAuthority is a trusted CA as loaded from the store.
type StoredAuthority struct {
	ID        int32
	Record    AuthorityRecord
	Issuing   bool
	CreatedAt time.Time
}

// AuthorityState describes a CA for the superadmin (rollover progress).
type AuthorityState struct {
	ID                int32
	Issuing           bool
	CreatedAt         time.Time
	RetiredAt         *time.Time
	RootFingerprint   string // SHA-256 of the root certificate, hex
	CertificatesInUse int64  // valid, unrevoked agent certificates issued by this CA
}

// CertificateRecord is an issued agent certificate.
type CertificateRecord struct {
	Serial    string
	Org       tenant.OrgID
	Node      identity.NodeID
	Authority int32 // issuing CA
	NotBefore time.Time
	NotAfter  time.Time
}

// Store persists CA material and issued certificates (implemented in adapters/postgres).
type Store interface {
	// TrustedAuthorities returns all CAs that are not retired, oldest first.
	TrustedAuthorities(ctx context.Context) ([]StoredAuthority, error)
	// InsertAuthority stores the first CA (ErrAuthorityExists if there is one).
	InsertAuthority(ctx context.Context, record AuthorityRecord) error
	// StartRollover stores a new issuing CA; the current one stays trusted (ErrRolloverInProgress
	// if more than one CA is trusted already).
	StartRollover(ctx context.Context, record AuthorityRecord, by identity.UserID) error
	// Retire stops trusting a CA (ErrAuthorityInUse, ErrIssuingAuthority, ErrUnknownAuthority).
	Retire(ctx context.Context, id int32, by identity.UserID, now time.Time) error
	// Authorities returns all CAs (also retired ones) with their certificates in use at now.
	Authorities(ctx context.Context, now time.Time) ([]AuthorityState, error)
	RecordCertificate(ctx context.Context, scope tenant.Scope, record CertificateRecord) error
	Revoke(ctx context.Context, scope tenant.Scope, serial string, at time.Time) (bool, error)
	Usable(ctx context.Context, serial string, org tenant.OrgID, node identity.NodeID, at time.Time) (bool, error)
}

type authority struct {
	id int32
	ca *pki.Authority
}

// Service is the agent CA.
type Service struct {
	store  Store
	box    *secrets.Box
	now    func() time.Time
	policy authz.Policy

	mu         sync.RWMutex
	trusted    []authority // oldest first
	issuing    authority
	generation uint64 // changes whenever the trusted set changes
}

// Bootstrap loads the trusted authorities or creates and stores the first one.
func Bootstrap(ctx context.Context, store Store, box *secrets.Box, now func() time.Time) (*Service, error) {
	s := &Service{store: store, box: box, now: now, policy: authz.NewPolicy()}
	if err := s.Refresh(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// Refresh reloads the trusted authorities (other server instances may have rolled the CA over).
func (s *Service) Refresh(ctx context.Context) error {
	stored, err := s.store.TrustedAuthorities(ctx)
	if err != nil {
		return fmt.Errorf("load certificate authorities: %w", err)
	}
	if len(stored) == 0 {
		if err := s.createFirst(ctx); err != nil {
			return err
		}
		return s.Refresh(ctx)
	}
	trusted := make([]authority, 0, len(stored))
	var issuing authority
	for _, st := range stored {
		ca, err := open(st.Record, s.box)
		if err != nil {
			return fmt.Errorf("certificate authority %d: %w", st.ID, err)
		}
		a := authority{id: st.ID, ca: ca}
		trusted = append(trusted, a)
		if st.Issuing {
			issuing = a
		}
	}
	if issuing.ca == nil {
		return errors.New("no issuing certificate authority")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !sameAuthorities(s.trusted, trusted) || s.issuing.id != issuing.id {
		s.generation++
	}
	s.trusted, s.issuing = trusted, issuing
	return nil
}

func (s *Service) createFirst(ctx context.Context) error {
	ca, err := pki.NewAuthority(s.now())
	if err != nil {
		return fmt.Errorf("create certificate authority: %w", err)
	}
	record, err := seal(ca, s.box)
	if err != nil {
		return err
	}
	if err := s.store.InsertAuthority(ctx, record); err != nil && !errors.Is(err, ErrAuthorityExists) {
		return fmt.Errorf("store certificate authority: %w", err)
	}
	return nil // created, or another instance won the race: Refresh loads it
}

func sameAuthorities(a, b []authority) bool {
	return slices.EqualFunc(a, b, func(x, y authority) bool { return x.id == y.id })
}

// Generation changes whenever the trusted CAs change (renew the server certificate then).
func (s *Service) Generation() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.generation
}

// TrustAnchor returns the root of the issuing CA.
func (s *Service) TrustAnchor() *x509.Certificate {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.issuing.ca.Root()
}

// Intermediate returns the issuing CA certificate (sent along with agent certificates).
func (s *Service) Intermediate() *x509.Certificate {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.issuing.ca.Intermediate()
}

// TrustAnchors returns the roots of all trusted CAs (the trust bundle for agents).
func (s *Service) TrustAnchors() []*x509.Certificate {
	s.mu.RLock()
	defer s.mu.RUnlock()
	roots := make([]*x509.Certificate, 0, len(s.trusted))
	for _, a := range s.trusted {
		roots = append(roots, a.ca.Root())
	}
	return roots
}

// Intermediates returns the issuing certificates of all trusted CAs.
func (s *Service) Intermediates() []*x509.Certificate {
	s.mu.RLock()
	defer s.mu.RUnlock()
	inters := make([]*x509.Certificate, 0, len(s.trusted))
	for _, a := range s.trusted {
		inters = append(inters, a.ca.Intermediate())
	}
	return inters
}

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

// SignAgentCertificate signs the CSR with the issuing CA without recording it. The caller must
// record the returned record in the same transaction that creates the node (enrollment); until
// then the certificate is unusable because the agent API accepts recorded serials only.
func (s *Service) SignAgentCertificate(org tenant.OrgID, csrDER []byte, node identity.NodeID) (pki.Issued, CertificateRecord, error) {
	s.mu.RLock()
	issuing := s.issuing
	s.mu.RUnlock()
	issued, err := issuing.ca.SignAgentCSR(csrDER, org, node, s.now())
	if err != nil {
		return pki.Issued{}, CertificateRecord{}, fmt.Errorf("sign agent certificate: %w", err)
	}
	record := CertificateRecord{Serial: issued.Serial, Org: org, Node: node, Authority: issuing.id, NotBefore: issued.NotBefore, NotAfter: issued.NotAfter}
	return issued, record, nil
}

// IssueServerCertificate issues a fresh server certificate for the agent API. During a rollover
// it comes from the oldest trusted CA: agents that have not received the new trust bundle yet
// still accept it. After the old CA is retired, it comes from the new one.
func (s *Service) IssueServerCertificate(hosts []string) (tls.Certificate, error) {
	s.mu.RLock()
	oldest := s.trusted[0]
	s.mu.RUnlock()
	cert, err := oldest.ca.IssueServerCertificate(hosts, s.now())
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

// CertificateUsable reports whether the certificate with serial was issued for org and node, is
// not revoked and is within its validity period (checked on every agent connection and again
// while it lasts; unknown serials are unusable).
func (s *Service) CertificateUsable(ctx context.Context, serial string, org tenant.OrgID, node identity.NodeID) (bool, error) {
	usable, err := s.store.Usable(ctx, serial, org, node, s.now())
	if err != nil {
		return false, fmt.Errorf("check agent certificate: %w", err)
	}
	return usable, nil
}

// Status returns all CAs with their rollover progress (superadmin).
func (s *Service) Status(ctx context.Context, c access.Caller) ([]AuthorityState, error) {
	if err := s.authorize(c, false); err != nil {
		return nil, err
	}
	states, err := s.store.Authorities(ctx, s.now())
	if err != nil {
		return nil, fmt.Errorf("load certificate authorities: %w", err)
	}
	return states, nil
}

// StartRollover creates a new issuing CA; the current one stays trusted until it is retired
// (superadmin, step-up).
func (s *Service) StartRollover(ctx context.Context, c access.Caller) error {
	if err := s.authorize(c, true); err != nil {
		return err
	}
	ca, err := pki.NewAuthority(s.now())
	if err != nil {
		return fmt.Errorf("create certificate authority: %w", err)
	}
	record, err := seal(ca, s.box)
	if err != nil {
		return err
	}
	if err := s.store.StartRollover(ctx, record, c.Principal.User); err != nil {
		return fmt.Errorf("start CA rollover: %w", err)
	}
	return s.Refresh(ctx)
}

// Retire stops trusting a previous CA once no valid certificate of it remains (superadmin, step-up).
func (s *Service) Retire(ctx context.Context, c access.Caller, id int32) error {
	if err := s.authorize(c, true); err != nil {
		return err
	}
	if err := s.store.Retire(ctx, id, c.Principal.User, s.now()); err != nil {
		return fmt.Errorf("retire CA: %w", err)
	}
	return s.Refresh(ctx)
}

func (s *Service) authorize(c access.Caller, stepUp bool) error {
	if !s.policy.AllowedInstance(c.Principal, authz.ManageAgentCA) {
		return access.ErrForbidden
	}
	if stepUp && s.now().Sub(c.StepUpAt) > access.StepUpMaxAge {
		return access.ErrStepUpRequired
	}
	return nil
}

// Fingerprint returns the hex SHA-256 of a certificate (shown to operators).
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
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
