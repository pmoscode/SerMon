package agentapi

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/pmoscode/SerMon/server/internal/adapters/pki"
)

// CA is the agent certificate authority as the agent API needs it (agentca.Service).
type CA interface {
	TrustAnchor() *x509.Certificate
	Intermediate() *x509.Certificate
	IssueServerCertificate(hosts []string) (tls.Certificate, error)
	// CertificateUsable reports whether a serial is recorded, not revoked and valid now.
	CertificateUsable(ctx context.Context, serial string) (bool, error)
}

// usableCheckTimeout bounds the revocation check during a handshake.
const usableCheckTimeout = 5 * time.Second

var errUnusableCertificate = errors.New("agent certificate is revoked or unknown")

// TLSConfig returns the TLS configuration of the agent API (KONZEPT §18.2): TLS 1.3 only,
// client certificate required, verified against the agent CA on every handshake (chain,
// client-auth usage, exactly one SPIFFE identity, recorded and not revoked). The server
// certificate is issued by the agent CA for hosts and renewed in memory after two thirds of
// its lifetime.
func TLSConfig(ca CA, hosts []string, logger *slog.Logger) (*tls.Config, error) {
	certs := &serverCertificates{ca: ca, hosts: hosts, logger: logger}
	if _, err := certs.get(nil); err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:     tls.VersionTLS13,
		ClientAuth:     tls.RequireAnyClientCert, // verified in VerifyConnection
		GetCertificate: certs.get,
		// VerifyConnection also runs for resumed sessions (unlike VerifyPeerCertificate), so a
		// revoked certificate cannot come back through session resumption. Tickets are disabled
		// as well: every connection is a full handshake with a fresh revocation check.
		VerifyConnection:       verifyAgent(ca, logger),
		SessionTicketsDisabled: true,
		NextProtos:             []string{"h2"},
	}, nil
}

func verifyAgent(ca CA, logger *slog.Logger) func(tls.ConnectionState) error {
	return func(state tls.ConnectionState) error {
		chain := state.PeerCertificates
		if len(chain) == 0 {
			return errors.New("client certificate required")
		}
		leaf := chain[0]
		roots, intermediates := x509.NewCertPool(), x509.NewCertPool()
		roots.AddCert(ca.TrustAnchor())
		intermediates.AddCert(ca.Intermediate())
		for _, c := range chain[1:] {
			intermediates.AddCert(c)
		}
		if _, err := leaf.Verify(x509.VerifyOptions{
			Roots: roots, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		}); err != nil {
			return fmt.Errorf("verify client certificate: %w", err)
		}
		org, node, err := pki.AgentIdentity(leaf)
		if err != nil {
			return fmt.Errorf("client certificate identity: %w", err)
		}
		serial := hex.EncodeToString(leaf.SerialNumber.Bytes())
		// The TLS callback has no context; the check gets its own deadline.
		ctx, cancel := context.WithTimeout(context.Background(), usableCheckTimeout)
		defer cancel()
		usable, err := ca.CertificateUsable(ctx, serial)
		if err != nil {
			return fmt.Errorf("check client certificate: %w", err)
		}
		if !usable {
			logger.Warn("agent rejected: certificate revoked or unknown", "org", org.String(), "node", node.String(), "serial", serial)
			return errUnusableCertificate
		}
		return nil
	}
}

// serverCertificates caches the agent API's server certificate and renews it in time.
type serverCertificates struct {
	ca     CA
	hosts  []string
	logger *slog.Logger
	mu     sync.Mutex
	cert   *tls.Certificate
}

func (s *serverCertificates) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if s.cert != nil && now.Before(renewAt(s.cert.Leaf)) {
		return s.cert, nil
	}
	fresh, err := s.ca.IssueServerCertificate(s.hosts)
	if err != nil {
		if s.cert != nil && now.Before(s.cert.Leaf.NotAfter) {
			s.logger.Error("renew agent API server certificate failed; keeping the current one", "error", err)
			return s.cert, nil
		}
		return nil, fmt.Errorf("agent API server certificate: %w", err)
	}
	s.cert = &fresh
	return s.cert, nil
}

// renewAt is two thirds into the certificate's lifetime.
func renewAt(c *x509.Certificate) time.Time {
	return c.NotBefore.Add(c.NotAfter.Sub(c.NotBefore) * 2 / 3)
}
