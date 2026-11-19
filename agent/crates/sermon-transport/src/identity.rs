//! The agent's key pair, its CSR and the check of certificates issued by the server.

use std::fmt;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use rcgen::{CertificateParams, DistinguishedName, KeyPair, PKCS_ECDSA_P256_SHA256, PublicKeyData};
use rustls::pki_types::{CertificateDer, TrustAnchor, UnixTime};
use webpki::{EndEntityCert, KeyUsage, anchor_from_trusted_cert};

/// Errors around the agent identity.
#[derive(Debug, thiserror::Error)]
pub enum IdentityError {
    /// Key generation or CSR encoding failed.
    #[error("cannot create the key or CSR: {0}")]
    Key(#[from] rcgen::Error),
    /// The certificate from the server is unusable; the reason says why.
    #[error("the server returned an unusable certificate: {0}")]
    InvalidCertificate(String),
}

/// The agent's ECDSA P-256 key pair. It never leaves the host and never appears in `Debug` output.
pub struct AgentKey {
    key_pair: KeyPair,
}

impl AgentKey {
    /// Generates a new key pair.
    ///
    /// # Errors
    ///
    /// [`IdentityError::Key`] if the system's random source fails.
    pub fn generate() -> Result<Self, IdentityError> {
        Ok(Self {
            key_pair: KeyPair::generate_for(&PKCS_ECDSA_P256_SHA256)?,
        })
    }

    /// Loads a key pair from PKCS#8 PEM.
    ///
    /// # Errors
    ///
    /// [`IdentityError::Key`] if the PEM is not a P-256 key.
    pub fn from_pem(pem: &str) -> Result<Self, IdentityError> {
        Ok(Self {
            key_pair: KeyPair::from_pem_and_sign_algo(pem, &PKCS_ECDSA_P256_SHA256)?,
        })
    }

    /// The private key as PKCS#8 PEM, for the key file (mode 0600).
    #[must_use]
    pub fn to_pem(&self) -> String {
        self.key_pair.serialize_pem()
    }

    /// The private key as PKCS#8 DER (for the TLS client configuration).
    #[must_use]
    pub fn to_pkcs8_der(&self) -> Vec<u8> {
        self.key_pair.serialize_der()
    }

    /// The public key as DER `SubjectPublicKeyInfo`.
    #[must_use]
    pub fn public_key_info(&self) -> Vec<u8> {
        self.key_pair.subject_public_key_info()
    }

    /// A PKCS#10 request (DER) for this key. Subject and extensions are left empty: the server
    /// ignores them and decides the identity itself.
    ///
    /// # Errors
    ///
    /// [`IdentityError::Key`] if signing the request fails.
    pub fn csr_der(&self) -> Result<Vec<u8>, IdentityError> {
        let mut params = CertificateParams::default();
        params.distinguished_name = DistinguishedName::new();
        let csr = params.serialize_request(&self.key_pair)?;
        Ok(csr.der().to_vec())
    }
}

impl fmt::Debug for AgentKey {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("AgentKey(***)")
    }
}

/// Whether `certificate` was issued for the public key of `key`.
#[must_use]
pub fn certificate_matches_key(certificate: &CertificateDer<'_>, key: &AgentKey) -> bool {
    EndEntityCert::try_from(certificate).is_ok_and(|cert| {
        cert.subject_public_key_info().as_ref() == key.public_key_info().as_slice()
    })
}

/// Organization and node of an agent, from the SPIFFE ID `spiffe://sermon/org/<org>/node/<node>`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct AgentIdentity {
    /// Organization UUID.
    pub org_id: String,
    /// Node UUID.
    pub node_id: String,
}

impl AgentIdentity {
    /// Parses a SPIFFE ID as issued by the server's agent CA (lowercase UUIDs).
    #[must_use]
    pub fn from_spiffe_id(uri: &str) -> Option<Self> {
        let rest = uri.strip_prefix("spiffe://sermon/org/")?;
        let (org_id, node_id) = rest.split_once("/node/")?;
        (is_uuid(org_id) && is_uuid(node_id)).then(|| Self {
            org_id: org_id.to_owned(),
            node_id: node_id.to_owned(),
        })
    }
}

fn is_uuid(text: &str) -> bool {
    let groups: Vec<&str> = text.split('-').collect();
    groups.iter().map(|group| group.len()).eq([8, 4, 4, 4, 12])
        && groups.iter().all(|group| {
            group
                .bytes()
                .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte))
        })
}

/// A client certificate issued by the server, checked against the agent's key.
#[derive(Debug, Clone)]
pub struct IssuedCertificate {
    /// Who the agent is now.
    pub identity: AgentIdentity,
    /// Client certificate (DER).
    pub certificate: CertificateDer<'static>,
    /// Issuing CA certificates (DER), sent along with the client certificate.
    pub intermediates: Vec<CertificateDer<'static>>,
    /// Roots that the server certificate of the agent API must chain to.
    pub trust_anchors: Vec<CertificateDer<'static>>,
    /// End of the certificate's validity.
    pub not_after: SystemTime,
}

/// Lifetime assumed for certificates without a known issue time (as issued by the server).
const DEFAULT_LIFETIME: Duration = Duration::from_hours(30 * 24);

/// Everything the agent needs for the mTLS agent API.
#[derive(Debug)]
pub struct Credentials {
    /// The agent's private key.
    pub key: AgentKey,
    /// Organization and node.
    pub identity: AgentIdentity,
    /// Client certificate.
    pub certificate: CertificateDer<'static>,
    /// Issuing CA certificates.
    pub intermediates: Vec<CertificateDer<'static>>,
    /// Roots for the server certificate of the agent API (trust bundle).
    pub trust_anchors: Vec<CertificateDer<'static>>,
    /// End of the certificate's validity.
    pub not_after: SystemTime,
    /// When the agent received the certificate, if known.
    pub issued_at: Option<SystemTime>,
}

impl Credentials {
    /// When to rotate: after two thirds of the lifetime (KONZEPT §6).
    #[must_use]
    pub fn rotate_at(&self) -> SystemTime {
        let lifetime = self
            .issued_at
            .and_then(|issued| self.not_after.duration_since(issued).ok())
            .unwrap_or(DEFAULT_LIFETIME);
        self.not_after
            .checked_sub(lifetime / 3)
            .unwrap_or(self.not_after)
    }

    /// Whether the certificate has expired at `now`.
    #[must_use]
    pub fn is_expired(&self, now: SystemTime) -> bool {
        now >= self.not_after
    }
}

/// What the server sent for a CSR: certificate, chain, roots and expiry.
#[derive(Debug, Clone)]
pub struct CertificateOffer<'a> {
    /// Client certificate (DER).
    pub certificate: &'a [u8],
    /// Issuing CA certificates (DER).
    pub intermediates: &'a [Vec<u8>],
    /// Roots (DER).
    pub trust_anchors: &'a [Vec<u8>],
    /// Expiry as stated by the server.
    pub not_after: Option<SystemTime>,
}

impl IssuedCertificate {
    /// Checks a certificate from the server before the agent stores or uses it:
    ///
    /// - it is for the agent's own public key,
    /// - it was signed by the issuing CA sent along and is valid now for client authentication,
    /// - it carries exactly one agent SPIFFE ID (and, if given, for `expected_node`),
    /// - the stated expiry lies in the future.
    ///
    /// # Errors
    ///
    /// [`IdentityError::InvalidCertificate`] naming the failed check.
    pub fn verify(
        key: &AgentKey,
        offer: &CertificateOffer<'_>,
        expected_node: Option<&str>,
        now: SystemTime,
    ) -> Result<Self, IdentityError> {
        let invalid = |reason: &str| IdentityError::InvalidCertificate(reason.to_owned());
        let certificate = CertificateDer::from(offer.certificate.to_vec());
        let intermediates: Vec<CertificateDer<'static>> = offer
            .intermediates
            .iter()
            .map(|der| CertificateDer::from(der.clone()))
            .collect();
        let trust_anchors: Vec<CertificateDer<'static>> = offer
            .trust_anchors
            .iter()
            .map(|der| CertificateDer::from(der.clone()))
            .collect();
        if trust_anchors.is_empty() {
            return Err(invalid("no trust anchor"));
        }

        let end_entity =
            EndEntityCert::try_from(&certificate).map_err(|_| invalid("not a certificate"))?;
        if !certificate_matches_key(&certificate, key) {
            return Err(invalid("issued for a different key"));
        }
        // The leaf must be a currently valid client certificate signed by the issuing CA the
        // server named. The CA's own name constraints are deliberately not evaluated here:
        // rustls-webpki rejects every URI SAN below a URI name constraint, and the agent CA
        // restricts itself to spiffe://sermon (M1-07). The server verifies the full chain
        // including the constraint (Go x509) on every handshake.
        let issuers = if intermediates.is_empty() {
            &trust_anchors
        } else {
            &intermediates
        };
        let anchors = issuers
            .iter()
            .map(|der| {
                anchor_from_trusted_cert(der).map(|anchor| TrustAnchor {
                    name_constraints: None,
                    ..anchor
                })
            })
            .collect::<Result<Vec<TrustAnchor<'_>>, _>>()
            .map_err(|_| invalid("malformed CA certificate"))?;
        let since_epoch = now.duration_since(UNIX_EPOCH).unwrap_or(Duration::ZERO);
        let provider = crate::tls::provider();
        end_entity
            .verify_for_usage(
                provider.signature_verification_algorithms.all,
                &anchors,
                &[],
                UnixTime::since_unix_epoch(since_epoch),
                KeyUsage::client_auth(),
                None,
                None,
            )
            .map_err(|error| {
                IdentityError::InvalidCertificate(format!("chain does not verify: {error}"))
            })?;

        let uris: Vec<&str> = end_entity.valid_uri_names().collect();
        let [uri] = uris.as_slice() else {
            return Err(invalid("expected exactly one SPIFFE ID"));
        };
        let identity =
            AgentIdentity::from_spiffe_id(uri).ok_or_else(|| invalid("not an agent SPIFFE ID"))?;
        if expected_node.is_some_and(|node| node != identity.node_id) {
            return Err(invalid("issued for a different node"));
        }
        let not_after = offer.not_after.ok_or_else(|| invalid("missing expiry"))?;
        if not_after <= now {
            return Err(invalid("already expired"));
        }

        Ok(Self {
            identity,
            certificate,
            intermediates,
            trust_anchors,
            not_after,
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn csr_is_signed_by_the_key_and_carries_no_identity() {
        let key = AgentKey::generate().unwrap();

        let csr = key.csr_der().unwrap();
        let parsed = rcgen::CertificateSigningRequestParams::from_der(&csr.clone().into());

        // rcgen verifies the CSR signature while parsing (feature x509-parser is test-only).
        let parsed = parsed.unwrap();
        assert_eq!(
            parsed.public_key.subject_public_key_info(),
            key.public_key_info()
        );
        assert_eq!(parsed.params.subject_alt_names.len(), 0);
        assert_eq!(parsed.params.distinguished_name.iter().count(), 0);
    }

    #[test]
    fn key_roundtrips_through_pem_and_is_redacted() {
        let key = AgentKey::generate().unwrap();

        let loaded = AgentKey::from_pem(&key.to_pem()).unwrap();

        assert_eq!(loaded.public_key_info(), key.public_key_info());
        assert!(key.to_pem().starts_with("-----BEGIN PRIVATE KEY-----"));
        assert_eq!(format!("{key:?}"), "AgentKey(***)");
    }

    #[test]
    fn every_key_is_new() {
        let first = AgentKey::generate().unwrap();
        let second = AgentKey::generate().unwrap();

        assert_ne!(first.public_key_info(), second.public_key_info());
    }

    fn credentials(issued_at: Option<SystemTime>, not_after: SystemTime) -> Credentials {
        Credentials {
            key: AgentKey::generate().unwrap(),
            identity: AgentIdentity {
                org_id: String::new(),
                node_id: String::new(),
            },
            certificate: CertificateDer::from(Vec::new()),
            intermediates: Vec::new(),
            trust_anchors: Vec::new(),
            not_after,
            issued_at,
        }
    }

    #[test]
    fn rotation_is_due_after_two_thirds_of_the_lifetime() {
        let issued = UNIX_EPOCH + Duration::from_hours(498_624);
        let day = Duration::from_hours(24);

        let thirty_days = credentials(Some(issued), issued + 30 * day);
        assert_eq!(thirty_days.rotate_at(), issued + 20 * day);

        let three_days = credentials(Some(issued), issued + 3 * day);
        assert_eq!(three_days.rotate_at(), issued + 2 * day);

        // Unknown issue time: the server's lifetime of 30 days is assumed.
        let unknown = credentials(None, issued + 30 * day);
        assert_eq!(unknown.rotate_at(), issued + 20 * day);
    }

    #[test]
    fn expiry_is_checked_against_the_given_time() {
        let not_after = UNIX_EPOCH + Duration::from_hours(498_624);
        let credentials = credentials(None, not_after);

        assert!(!credentials.is_expired(not_after - Duration::from_secs(1)));
        assert!(credentials.is_expired(not_after));
    }

    #[test]
    fn parses_agent_spiffe_ids() {
        let org = "0192f6c8-1111-7000-8000-000000000001";
        let node = "0192f6c8-2222-7000-8000-000000000002";

        assert_eq!(
            AgentIdentity::from_spiffe_id(&format!("spiffe://sermon/org/{org}/node/{node}")),
            Some(AgentIdentity {
                org_id: org.into(),
                node_id: node.into()
            })
        );
        for bad in [
            format!("spiffe://other/org/{org}/node/{node}"),
            format!("spiffe://sermon/org/{org}/node/{node}/x"),
            format!("spiffe://sermon/org/{}/node/{node}", org.to_uppercase()),
            format!("spiffe://sermon/org/{org}/node/"),
            format!("spiffe://sermon/node/{node}"),
            "spiffe://sermon/org/x/node/y".to_owned(),
        ] {
            assert_eq!(AgentIdentity::from_spiffe_id(&bad), None, "{bad}");
        }
    }
}
