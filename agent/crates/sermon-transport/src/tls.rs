//! TLS client configurations for the server endpoints.

use std::sync::Arc;

use rustls::client::WebPkiServerVerifier;
use rustls::client::danger::{HandshakeSignatureValid, ServerCertVerified, ServerCertVerifier};
use rustls::crypto::{CryptoProvider, verify_tls12_signature, verify_tls13_signature};
use rustls::pki_types::{CertificateDer, ServerName, UnixTime};
use rustls::{ClientConfig, DigitallySignedStruct, RootCertStore, SignatureScheme};

use crate::fingerprint::Fingerprint;

/// ALPN protocol of gRPC.
const ALPN_H2: &[u8] = b"h2";

/// How the agent checks the certificate of the public HTTPS endpoint at enrollment (KONZEPT §6).
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum ServerTrust {
    /// The certificate must chain to a CA of the operating system (e.g. Let's Encrypt via Caddy).
    SystemRoots,
    /// A certificate presented by the server must have this fingerprint (self-signed setups). If it
    /// is a CA certificate, the chain and the host name are verified against it.
    Pinned(Fingerprint),
}

/// Errors when building a TLS configuration.
#[derive(Debug, thiserror::Error)]
pub enum TlsError {
    /// The operating system has no usable CA certificates.
    #[error(
        "no CA certificates found on this system – install ca-certificates or use --ca-fingerprint"
    )]
    NoSystemRoots,
    /// rustls refused the configuration.
    #[error("invalid TLS configuration: {0}")]
    Rustls(#[from] rustls::Error),
}

/// The crypto provider of the agent (ring; no OpenSSL, no C toolchain for musl).
#[must_use]
pub fn provider() -> Arc<CryptoProvider> {
    Arc::new(rustls::crypto::ring::default_provider())
}

/// TLS configuration for the enrollment request to the public HTTPS endpoint (ADR 0009).
///
/// TLS 1.2 is allowed here because the endpoint is the public web server; the mTLS agent API is
/// TLS 1.3 only.
///
/// # Errors
///
/// [`TlsError::NoSystemRoots`] if [`ServerTrust::SystemRoots`] finds no CA certificates.
pub fn enrollment_config(trust: &ServerTrust) -> Result<Arc<ClientConfig>, TlsError> {
    let provider = provider();
    let builder = ClientConfig::builder_with_provider(provider.clone())
        .with_safe_default_protocol_versions()?;
    let mut config = match trust {
        ServerTrust::SystemRoots => builder
            .with_root_certificates(system_roots()?)
            .with_no_client_auth(),
        ServerTrust::Pinned(pin) => builder
            .dangerous()
            .with_custom_certificate_verifier(Arc::new(PinnedVerifier {
                pin: *pin,
                provider,
            }))
            .with_no_client_auth(),
    };
    config.alpn_protocols = vec![ALPN_H2.to_vec()];
    Ok(Arc::new(config))
}

fn system_roots() -> Result<RootCertStore, TlsError> {
    let mut roots = RootCertStore::empty();
    // Unreadable single files are skipped; only "nothing usable at all" is an error.
    let found = rustls_native_certs::load_native_certs();
    roots.add_parsable_certificates(found.certs);
    if roots.is_empty() {
        return Err(TlsError::NoSystemRoots);
    }
    Ok(roots)
}

/// Accepts the server if one of its certificates has the pinned fingerprint.
///
/// - Pin = leaf certificate: accepted like an SSH host key (no chain or name check needed).
/// - Pin = a CA certificate in the presented chain: the chain is verified with this CA as the only
///   trust anchor, including validity and host name.
#[derive(Debug)]
struct PinnedVerifier {
    pin: Fingerprint,
    provider: Arc<CryptoProvider>,
}

impl ServerCertVerifier for PinnedVerifier {
    fn verify_server_cert(
        &self,
        end_entity: &CertificateDer<'_>,
        intermediates: &[CertificateDer<'_>],
        server_name: &ServerName<'_>,
        ocsp_response: &[u8],
        now: UnixTime,
    ) -> Result<ServerCertVerified, rustls::Error> {
        if Fingerprint::of(end_entity) == self.pin {
            return Ok(ServerCertVerified::assertion());
        }
        if let Some(anchor) = intermediates
            .iter()
            .find(|cert| Fingerprint::of(cert) == self.pin)
        {
            let mut roots = RootCertStore::empty();
            roots.add(anchor.clone().into_owned())?;
            let verifier =
                WebPkiServerVerifier::builder_with_provider(Arc::new(roots), self.provider.clone())
                    .build()
                    .map_err(|error| rustls::Error::General(error.to_string()))?;
            return verifier.verify_server_cert(
                end_entity,
                intermediates,
                server_name,
                ocsp_response,
                now,
            );
        }
        let presented: Vec<String> = std::iter::once(end_entity)
            .chain(intermediates)
            .map(|cert| Fingerprint::of(cert).to_string())
            .collect();
        Err(rustls::Error::General(format!(
            "no server certificate matches the pinned fingerprint {}; the server presented {}",
            self.pin,
            presented.join(", ")
        )))
    }

    fn verify_tls12_signature(
        &self,
        message: &[u8],
        cert: &CertificateDer<'_>,
        dss: &DigitallySignedStruct,
    ) -> Result<HandshakeSignatureValid, rustls::Error> {
        verify_tls12_signature(
            message,
            cert,
            dss,
            &self.provider.signature_verification_algorithms,
        )
    }

    fn verify_tls13_signature(
        &self,
        message: &[u8],
        cert: &CertificateDer<'_>,
        dss: &DigitallySignedStruct,
    ) -> Result<HandshakeSignatureValid, rustls::Error> {
        verify_tls13_signature(
            message,
            cert,
            dss,
            &self.provider.signature_verification_algorithms,
        )
    }

    fn supported_verify_schemes(&self) -> Vec<SignatureScheme> {
        self.provider
            .signature_verification_algorithms
            .supported_schemes()
    }
}
