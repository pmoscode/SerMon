//! Signed agent releases for the self-update (ADR 0036): fetched over the agent API (mTLS),
//! checked by the root helper, never by the agent.

use std::sync::Arc;
use std::time::Duration;

use http::Uri;
use tonic::codec::CompressionEncoding;

use crate::channel;
use crate::proto::agent::v1::agent_service_client::AgentServiceClient;
use crate::proto::agent::v1::fetch_agent_release_response::Part;
use crate::proto::agent::v1::{Architecture, FetchAgentReleaseRequest, ReleaseArtifact};
use crate::session::IdentitySource;
use crate::tls::agent_api_config;

/// The largest binary the helper accepts (ADR 0036).
pub const MAX_BINARY: u64 = 64 << 20;
/// The largest signature file.
pub const MAX_SIGNATURE: usize = 4 << 10;
const TIMEOUT: Duration = Duration::from_mins(5);

/// A release as the server sent it.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Release {
    pub binary: Vec<u8>,
    pub signature: String,
}

/// Fetches releases with the agent's identity.
#[derive(Clone)]
pub struct ReleaseFetcher {
    url: Uri,
    identity: Arc<dyn IdentitySource>,
}

impl std::fmt::Debug for ReleaseFetcher {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("ReleaseFetcher")
            .field("url", &self.url)
            .finish_non_exhaustive()
    }
}

fn len(n: usize) -> u64 {
    u64::try_from(n).unwrap_or(u64::MAX)
}

/// The architecture of this binary.
#[must_use]
pub fn architecture() -> Architecture {
    if cfg!(target_arch = "aarch64") {
        Architecture::Arm64
    } else {
        Architecture::Amd64
    }
}

impl ReleaseFetcher {
    #[must_use]
    pub fn new(url: Uri, identity: Arc<dyn IdentitySource>) -> Self {
        Self { url, identity }
    }

    /// Fetches the release `version` of this architecture.
    ///
    /// # Errors
    ///
    /// Why it could not be fetched, as text for the run's result.
    pub async fn fetch(&self, version: &str) -> Result<Release, String> {
        self.fetch_artifact(version, ReleaseArtifact::Agent)
            .await?
            .ok_or_else(|| "the server refused the release: no such release".to_owned())
    }

    /// Fetches the root helper of the release `version` (M4-31); `None` from a server or a release
    /// without one.
    ///
    /// # Errors
    ///
    /// Why it could not be fetched, as text for the run's result.
    pub async fn fetch_helper(&self, version: &str) -> Result<Option<Release>, String> {
        self.fetch_artifact(version, ReleaseArtifact::Helper).await
    }

    async fn fetch_artifact(
        &self,
        version: &str,
        artifact: ReleaseArtifact,
    ) -> Result<Option<Release>, String> {
        tokio::time::timeout(TIMEOUT, self.fetch_now(version, artifact))
            .await
            .map_err(|_| "fetching the release took too long".to_owned())?
    }

    async fn fetch_now(
        &self,
        version: &str,
        artifact: ReleaseArtifact,
    ) -> Result<Option<Release>, String> {
        let credentials = self
            .identity
            .load()
            .map_err(|e| format!("cannot load the identity: {e}"))?;
        let tls =
            agent_api_config(&credentials).map_err(|e| format!("unusable credentials: {e}"))?;
        let channel = channel::connect(self.url.clone(), Some(tls), None)
            .await
            .map_err(|e| format!("cannot connect: {e}"))?;
        let mut client =
            AgentServiceClient::new(channel).accept_compressed(CompressionEncoding::Gzip);
        let request = FetchAgentReleaseRequest {
            version: version.to_owned(),
            architecture: architecture().into(),
            artifact: artifact.into(),
        };
        let mut stream = match client.fetch_agent_release(request).await {
            Ok(response) => response.into_inner(),
            Err(s) if s.code() == tonic::Code::NotFound => return Ok(None),
            Err(s) => return Err(format!("the server refused the release: {}", s.message())),
        };
        let first = stream.message().await.map_err(|s| s.message().to_owned())?;
        let Some(Part::Header(header)) = first.and_then(|m| m.part) else {
            return Err("the server sent no header".into());
        };
        if header.size > MAX_BINARY || header.signature.len() > MAX_SIGNATURE {
            return Err("the release is larger than allowed".into());
        }
        let mut binary = Vec::with_capacity(usize::try_from(header.size).unwrap_or(0));
        while let Some(message) = stream.message().await.map_err(|s| s.message().to_owned())? {
            let Some(Part::Chunk(chunk)) = message.part else {
                return Err("a second header".into());
            };
            if len(binary.len() + chunk.len()) > header.size {
                return Err("more bytes than announced".into());
            }
            binary.extend_from_slice(&chunk);
        }
        if len(binary.len()) != header.size {
            return Err("fewer bytes than announced".into());
        }
        Ok(Some(Release {
            binary,
            signature: header.signature,
        }))
    }
}
