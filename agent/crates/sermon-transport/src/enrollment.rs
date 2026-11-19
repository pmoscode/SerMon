//! Enrollment: exchange an enrollment token and a CSR for the agent's client certificate.

use std::time::SystemTime;

use sermon_core::{Clock, HostFacts};
use tonic::Code;
use tonic::transport::Channel;

use crate::identity::{AgentKey, CertificateOffer, IdentityError, IssuedCertificate};
use crate::proto::agent::v1::enrollment_service_client::EnrollmentServiceClient;
use crate::proto::agent::v1::{EnrollRequest, EnrollResponse};

/// Longest server message shown to the user.
const MAX_SERVER_MESSAGE_CHARS: usize = 300;

/// Why the enrollment failed, with a hint what to do.
#[derive(Debug, thiserror::Error)]
pub enum EnrollError {
    /// The token is wrong, expired, revoked or used up. The server doesn't say which (ADR 0009).
    #[error(
        "the enrollment token is invalid, expired, revoked or used up – create a new one in the UI"
    )]
    InvalidToken,
    /// The organization already has a node with this machine-id; the token was not used.
    #[error(
        "this host (same machine-id) is already registered in the organization – confirm the \
         takeover in the UI or remove the old node first"
    )]
    AlreadyEnrolled,
    /// Too many attempts from this address.
    #[error("too many enrollment attempts – try again in a minute")]
    RateLimited,
    /// The server rejected the request as invalid.
    #[error("the server rejected the request: {0}")]
    Rejected(String),
    /// The server could not be reached or didn't answer in time.
    #[error("the server is not reachable: {0}")]
    Unavailable(String),
    /// Any other error status.
    #[error("enrollment failed ({code:?}): {message}")]
    Server {
        /// gRPC status code.
        code: Code,
        /// Server message (sanitized).
        message: String,
    },
    /// Key or CSR could not be created, or the issued certificate is unusable.
    #[error(transparent)]
    Identity(#[from] IdentityError),
}

/// Enrolls the host: sends token, CSR and host facts, checks and returns the issued certificate.
///
/// # Errors
///
/// [`EnrollError`] for a refused request or an unusable answer.
pub async fn enroll(
    channel: Channel,
    key: &AgentKey,
    token: &str,
    facts: &HostFacts,
    agent_version: &str,
    clock: &dyn Clock,
) -> Result<IssuedCertificate, EnrollError> {
    let request = EnrollRequest {
        token: token.to_owned(),
        csr: key.csr_der()?,
        host_facts: Some(facts.into()),
        agent_version: agent_version.to_owned(),
    };
    let response = EnrollmentServiceClient::new(channel)
        .enroll(request)
        .await
        .map_err(|status| map_status(&status))?
        .into_inner();
    verify_response(key, &response, clock.now())
}

fn verify_response(
    key: &AgentKey,
    response: &EnrollResponse,
    now: SystemTime,
) -> Result<IssuedCertificate, EnrollError> {
    // Older servers only sent trust_anchor; trust_anchors is its superset.
    let single_anchor = [response.trust_anchor.clone()];
    let trust_anchors: &[Vec<u8>] = match (&response.trust_anchors, &response.trust_anchor) {
        (all, _) if !all.is_empty() => all,
        (_, single) if !single.is_empty() => &single_anchor,
        _ => &[],
    };
    let offer = CertificateOffer {
        certificate: &response.certificate,
        intermediates: &response.intermediates,
        trust_anchors,
        not_after: response
            .not_after
            .and_then(|timestamp| SystemTime::try_from(timestamp).ok()),
    };
    Ok(IssuedCertificate::verify(
        key,
        &offer,
        Some(&response.node_id),
        now,
    )?)
}

fn map_status(status: &tonic::Status) -> EnrollError {
    let message = sanitize(status.message());
    match status.code() {
        Code::Unauthenticated => EnrollError::InvalidToken,
        Code::AlreadyExists => EnrollError::AlreadyEnrolled,
        Code::ResourceExhausted => EnrollError::RateLimited,
        Code::InvalidArgument => EnrollError::Rejected(message),
        Code::Unavailable | Code::DeadlineExceeded => {
            EnrollError::Unavailable(match std::error::Error::source(status) {
                Some(source) => crate::channel::error_chain(source),
                None => message,
            })
        }
        code => EnrollError::Server { code, message },
    }
}

/// Server messages are shown in a terminal: no control characters (escape sequences), bounded.
fn sanitize(message: &str) -> String {
    crate::channel::printable(message, MAX_SERVER_MESSAGE_CHARS)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn maps_status_codes_to_actionable_errors() {
        let cases = [
            (
                Code::Unauthenticated,
                "invalid, expired, revoked or used up",
            ),
            (Code::AlreadyExists, "already registered"),
            (Code::ResourceExhausted, "try again in a minute"),
            (Code::InvalidArgument, "rejected the request: details"),
            (Code::Unavailable, "not reachable: details"),
            (Code::Internal, "enrollment failed (Internal): details"),
        ];
        for (code, expected) in cases {
            let error = map_status(&tonic::Status::new(code, "details"));
            assert!(error.to_string().contains(expected), "{code:?}: {error}");
        }
    }

    #[test]
    fn server_messages_lose_control_characters_and_are_bounded() {
        let error = map_status(&tonic::Status::invalid_argument(format!(
            "bad\u{1b}[2J{}",
            "x".repeat(1000)
        )));

        let text = error.to_string();
        assert!(!text.contains('\u{1b}'));
        assert!(text.len() < 400, "{}", text.len());
    }
}
