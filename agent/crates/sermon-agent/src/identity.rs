//! The agent identity on disk: `<state_dir>/identity/` with key, certificates and metadata.
//!
//! Files are written with mode 0600 into a directory with mode 0700 and get the owner of the
//! state directory, so that `enroll` may run as root while the agent runs as `sermon`. A new
//! identity replaces the old one as a whole (rename), never file by file.

use std::fs::{self, DirBuilder, File, OpenOptions};
use std::io::{self, Write};
use std::os::unix::fs::{DirBuilderExt, MetadataExt, OpenOptionsExt};
use std::path::{Path, PathBuf};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use rustls::pki_types::CertificateDer;
use serde::{Deserialize, Serialize};
use sermon_transport::identity::{AgentIdentity, AgentKey, Credentials, IssuedCertificate};
use sermon_transport::session::{BoxError, IdentitySource};

use crate::state::{CERTIFICATE_FILE, IDENTITY_FILE, KEY_FILE, StateLayout, TRUST_BUNDLE_FILE};

/// Permission bits that must not be set on the key file (group and others).
const FOREIGN_ACCESS: u32 = 0o077;

/// Errors when storing or loading the identity.
#[derive(Debug, thiserror::Error)]
pub enum IdentityStoreError {
    /// A file or directory could not be read or written.
    #[error("{path}: {source}")]
    Io {
        /// The file or directory.
        path: PathBuf,
        /// Cause.
        #[source]
        source: io::Error,
    },
    /// A file exists but has unusable content or unsafe permissions.
    #[error("{path}: {reason}")]
    Invalid {
        /// The file.
        path: PathBuf,
        /// What is wrong.
        reason: String,
    },
}

/// The identity as loaded from disk.
pub type StoredIdentity = Credentials;

#[derive(Debug, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct Metadata {
    org_id: String,
    node_id: String,
    /// Expiry of the client certificate, seconds since the Unix epoch.
    not_after: u64,
    /// When the agent received the certificate (for the rotation time), seconds since the epoch.
    #[serde(default)]
    issued_at: Option<u64>,
}

/// Whether the host is enrolled (a client certificate exists).
#[must_use]
pub fn is_enrolled(layout: &StateLayout) -> bool {
    layout.certificate_file().exists()
}

/// Stores a new identity, replacing an existing one as a whole.
///
/// # Errors
///
/// [`IdentityStoreError::Io`] if writing fails; the previous identity then stays in place.
pub fn store(
    layout: &StateLayout,
    key: &AgentKey,
    issued: &IssuedCertificate,
    issued_at: SystemTime,
) -> Result<(), IdentityStoreError> {
    let root = layout.root();
    create_private_dir_all(root)?;
    check_writable_as(root)?;
    let pid = std::process::id();
    let staging = root.join(format!(".identity.new.{pid}"));
    let previous = root.join(format!(".identity.old.{pid}"));
    let target = layout.identity_dir();

    remove_if_exists(&staging)?;
    let result = write_staging(&staging, key, issued, issued_at)
        .and_then(|()| swap_in(&staging, &target, &previous))
        .and_then(|()| sync_dir(root));
    if result.is_err() {
        let _ = fs::remove_dir_all(&staging);
    }
    result
}

fn write_staging(
    staging: &Path,
    key: &AgentKey,
    issued: &IssuedCertificate,
    issued_at: SystemTime,
) -> Result<(), IdentityStoreError> {
    DirBuilder::new()
        .mode(0o700)
        .create(staging)
        .map_err(io_at(staging))?;

    let chain: Vec<&CertificateDer<'_>> = std::iter::once(&issued.certificate)
        .chain(&issued.intermediates)
        .collect();
    let metadata = Metadata {
        org_id: issued.identity.org_id.clone(),
        node_id: issued.identity.node_id.clone(),
        not_after: unix_seconds(issued.not_after),
        issued_at: Some(unix_seconds(issued_at)),
    };
    let metadata = toml::to_string(&metadata).map_err(|error| IdentityStoreError::Invalid {
        path: staging.join(IDENTITY_FILE),
        reason: error.to_string(),
    })?;

    let anchors: Vec<&CertificateDer<'_>> = issued.trust_anchors.iter().collect();
    write_private(&staging.join(KEY_FILE), key.to_pem().as_bytes())?;
    write_private(
        &staging.join(CERTIFICATE_FILE),
        certificates_pem(&chain).as_bytes(),
    )?;
    write_private(
        &staging.join(TRUST_BUNDLE_FILE),
        certificates_pem(&anchors).as_bytes(),
    )?;
    write_private(&staging.join(IDENTITY_FILE), metadata.as_bytes())?;
    sync_dir(staging)
}

/// Moves `staging` to `target`; an existing `target` is moved aside first and restored on failure.
fn swap_in(staging: &Path, target: &Path, previous: &Path) -> Result<(), IdentityStoreError> {
    remove_if_exists(previous)?;
    let had_previous = match fs::rename(target, previous) {
        Ok(()) => true,
        Err(error) if error.kind() == io::ErrorKind::NotFound => false,
        Err(error) => return Err(io_at(target)(error)),
    };
    if let Err(error) = fs::rename(staging, target) {
        if had_previous {
            let _ = fs::rename(previous, target);
        }
        return Err(io_at(target)(error));
    }
    if had_previous {
        fs::remove_dir_all(previous).map_err(io_at(previous))?;
    }
    Ok(())
}

/// Refuses to write as root into a directory another user controls: that user could replace the
/// staging directory with a symlink between our calls and make root write keys and change owners
/// elsewhere (privilege escalation of a compromised agent). `install.sh` therefore runs `enroll` as
/// the service user; files are always owned by the user who writes them.
fn check_writable_as(dir: &Path) -> Result<(), IdentityStoreError> {
    let meta = fs::metadata(dir).map_err(io_at(dir))?;
    root_may_write(
        rustix::process::geteuid().is_root(),
        meta.uid(),
        meta.mode(),
    )
    .map_err(|reason| invalid(dir, &reason))
}

/// The rule of [`check_writable_as`]: root only writes into directories that root owns and nobody
/// else may write to.
fn root_may_write(running_as_root: bool, dir_uid: u32, dir_mode: u32) -> Result<(), String> {
    if !running_as_root {
        return Ok(());
    }
    if dir_uid != 0 {
        return Err(format!(
            "belongs to uid {dir_uid}, not root – run enroll as the user that owns it, e.g. `sudo -u sermon sermon-agent enroll …`"
        ));
    }
    if dir_mode & 0o022 != 0 {
        return Err(
            "is writable by group or others – fix the permissions (0750 or stricter)".into(),
        );
    }
    Ok(())
}

/// Loads the identity written by [`store`].
///
/// # Errors
///
/// [`IdentityStoreError`] if a file is missing, malformed, the key file is readable by others or
/// the key doesn't belong to the certificate.
pub fn load(layout: &StateLayout) -> Result<StoredIdentity, IdentityStoreError> {
    let key_path = layout.key_file();
    let mode = fs::metadata(&key_path).map_err(io_at(&key_path))?.mode();
    if mode & FOREIGN_ACCESS != 0 {
        return Err(IdentityStoreError::Invalid {
            path: key_path,
            reason: format!(
                "readable by group or others (mode {:o}); fix with chmod 600",
                mode & 0o777
            ),
        });
    }
    let key =
        AgentKey::from_pem(&read_text(&key_path)?).map_err(|error| invalid(&key_path, &error))?;

    let certificate_path = layout.certificate_file();
    let mut chain = read_certificates(&certificate_path)?.into_iter();
    let certificate = chain
        .next()
        .ok_or_else(|| invalid(&certificate_path, &"no certificate"))?;
    let intermediates: Vec<_> = chain.collect();
    if !sermon_transport::identity::certificate_matches_key(&certificate, &key) {
        return Err(invalid(&certificate_path, &"does not belong to agent.key"));
    }

    let trust_path = layout.trust_bundle_file();
    let trust_anchors = read_certificates(&trust_path)?;
    if trust_anchors.is_empty() {
        return Err(invalid(&trust_path, &"no certificate"));
    }

    let metadata_path = layout.identity_file();
    let metadata: Metadata = toml::from_str(&read_text(&metadata_path)?)
        .map_err(|error| invalid(&metadata_path, &error))?;
    let identity = AgentIdentity {
        org_id: metadata.org_id,
        node_id: metadata.node_id,
    };
    Ok(Credentials {
        key,
        identity,
        certificate,
        intermediates,
        trust_anchors,
        not_after: from_unix(metadata.not_after)
            .ok_or_else(|| invalid(&metadata_path, &"not_after is out of range"))?,
        issued_at: metadata.issued_at.and_then(from_unix),
    })
}

/// Replaces the trust bundle (roots from the server) atomically.
///
/// # Errors
///
/// [`IdentityStoreError::Io`] if writing fails; the old bundle then stays in place.
pub fn store_trust_bundle(
    layout: &StateLayout,
    roots: &[CertificateDer<'static>],
) -> Result<(), IdentityStoreError> {
    check_writable_as(layout.root())?;
    check_writable_as(&layout.identity_dir())?;
    let target = layout.trust_bundle_file();
    let temporary = layout
        .identity_dir()
        .join(format!(".{TRUST_BUNDLE_FILE}.new"));
    match fs::remove_file(&temporary) {
        Err(error) if error.kind() != io::ErrorKind::NotFound => {
            return Err(io_at(&temporary)(error));
        }
        _ => {}
    }
    let anchors: Vec<&CertificateDer<'_>> = roots.iter().collect();
    let result = write_private(&temporary, certificates_pem(&anchors).as_bytes())
        .and_then(|()| fs::rename(&temporary, &target).map_err(io_at(&target)))
        .and_then(|()| sync_dir(&layout.identity_dir()));
    if result.is_err() {
        let _ = fs::remove_file(&temporary);
    }
    result
}

/// The identity files as [`IdentitySource`] for the transport.
#[derive(Debug, Clone)]
pub struct FileIdentity {
    layout: StateLayout,
}

impl FileIdentity {
    /// The identity below `layout`.
    #[must_use]
    pub fn new(layout: StateLayout) -> Self {
        Self { layout }
    }
}

impl IdentitySource for FileIdentity {
    fn load(&self) -> Result<Credentials, BoxError> {
        Ok(load(&self.layout)?)
    }

    fn store_rotated(
        &self,
        key: &AgentKey,
        issued: &IssuedCertificate,
        issued_at: SystemTime,
    ) -> Result<(), BoxError> {
        Ok(store(&self.layout, key, issued, issued_at)?)
    }

    fn store_trust_bundle(&self, roots: &[CertificateDer<'static>]) -> Result<(), BoxError> {
        Ok(store_trust_bundle(&self.layout, roots)?)
    }
}

fn from_unix(seconds: u64) -> Option<SystemTime> {
    UNIX_EPOCH.checked_add(Duration::from_secs(seconds))
}

fn unix_seconds(time: SystemTime) -> u64 {
    time.duration_since(UNIX_EPOCH)
        .unwrap_or(Duration::ZERO)
        .as_secs()
}

fn certificates_pem(certificates: &[&CertificateDer<'_>]) -> String {
    let blocks: Vec<pem::Pem> = certificates
        .iter()
        .map(|der| pem::Pem::new("CERTIFICATE", der.to_vec()))
        .collect();
    pem::encode_many_config(
        &blocks,
        pem::EncodeConfig::new().set_line_ending(pem::LineEnding::LF),
    )
}

fn read_certificates(path: &Path) -> Result<Vec<CertificateDer<'static>>, IdentityStoreError> {
    let blocks = pem::parse_many(read_text(path)?).map_err(|error| invalid(path, &error))?;
    blocks
        .into_iter()
        .map(|block| {
            if block.tag() == "CERTIFICATE" {
                Ok(CertificateDer::from(block.into_contents()))
            } else {
                Err(invalid(
                    path,
                    &format!("unexpected PEM block {}", block.tag()),
                ))
            }
        })
        .collect()
}

fn read_text(path: &Path) -> Result<String, IdentityStoreError> {
    fs::read_to_string(path).map_err(io_at(path))
}

fn write_private(path: &Path, contents: &[u8]) -> Result<(), IdentityStoreError> {
    let mut file = OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(0o600)
        .open(path)
        .map_err(io_at(path))?;
    file.write_all(contents).map_err(io_at(path))?;
    file.sync_all().map_err(io_at(path))
}

fn create_private_dir_all(path: &Path) -> Result<(), IdentityStoreError> {
    DirBuilder::new()
        .recursive(true)
        .mode(0o700)
        .create(path)
        .map_err(io_at(path))
}

fn remove_if_exists(path: &Path) -> Result<(), IdentityStoreError> {
    match fs::remove_dir_all(path) {
        Err(error) if error.kind() != io::ErrorKind::NotFound => Err(io_at(path)(error)),
        _ => Ok(()),
    }
}

fn sync_dir(path: &Path) -> Result<(), IdentityStoreError> {
    File::open(path)
        .and_then(|dir| dir.sync_all())
        .map_err(io_at(path))
}

fn io_at(path: &Path) -> impl Fn(io::Error) -> IdentityStoreError + '_ {
    move |source| IdentityStoreError::Io {
        path: path.to_owned(),
        source,
    }
}

fn invalid(path: &Path, reason: &dyn std::fmt::Display) -> IdentityStoreError {
    IdentityStoreError::Invalid {
        path: path.to_owned(),
        reason: reason.to_string(),
    }
}

#[cfg(test)]
mod tests {
    use std::os::unix::fs::PermissionsExt;

    use super::*;

    fn issued_for(key: &AgentKey) -> IssuedCertificate {
        // A self-signed stand-in: the store doesn't verify chains, only key and format.
        let params = rcgen::CertificateParams::new(Vec::<String>::new()).unwrap();
        let key_pair = rcgen::KeyPair::from_pem(&key.to_pem()).unwrap();
        let certificate = params.self_signed(&key_pair).unwrap().der().clone();
        let root = rcgen::CertificateParams::new(Vec::<String>::new())
            .unwrap()
            .self_signed(&rcgen::KeyPair::generate().unwrap())
            .unwrap()
            .der()
            .clone();
        IssuedCertificate {
            identity: AgentIdentity {
                org_id: "0192f6c8-1111-7000-8000-000000000001".into(),
                node_id: "0192f6c8-2222-7000-8000-000000000002".into(),
            },
            certificate,
            intermediates: vec![root.clone()],
            trust_anchors: vec![root],
            not_after: UNIX_EPOCH + Duration::from_hours(499_344),
        }
    }

    fn mode(path: &Path) -> u32 {
        fs::metadata(path).unwrap().permissions().mode() & 0o777
    }

    #[test]
    fn root_writes_only_into_directories_nobody_else_controls() {
        // As the owner itself everything is allowed (the normal case: enroll as `sermon`).
        assert!(root_may_write(false, 1000, 0o750).is_ok());
        assert!(root_may_write(true, 0, 0o700).is_ok());
        assert!(root_may_write(true, 0, 0o755).is_ok());
        // root in a directory of another user: that user could swap entries for symlinks.
        let error = root_may_write(true, 999, 0o750).unwrap_err();
        assert!(
            error.contains("run enroll as the user that owns it"),
            "{error}"
        );
        // root-owned but writable by group or others: the same.
        assert!(root_may_write(true, 0, 0o770).is_err());
        assert!(root_may_write(true, 0, 0o1777).is_err());
    }

    #[test]
    fn stores_and_loads_the_identity_with_private_permissions() {
        let dir = tempfile::tempdir().unwrap();
        let layout = StateLayout::new(dir.path().join("state"));
        let key = AgentKey::generate().unwrap();
        let issued = issued_for(&key);

        store(
            &layout,
            &key,
            &issued,
            UNIX_EPOCH + Duration::from_hours(498_000),
        )
        .unwrap();

        assert!(is_enrolled(&layout));
        assert_eq!(mode(layout.root()), 0o700);
        assert_eq!(mode(&layout.identity_dir()), 0o700);
        for file in [
            layout.key_file(),
            layout.certificate_file(),
            layout.trust_bundle_file(),
            layout.identity_file(),
        ] {
            assert_eq!(mode(&file), 0o600, "{}", file.display());
        }
        let loaded = load(&layout).unwrap();
        assert_eq!(loaded.key.public_key_info(), key.public_key_info());
        assert_eq!(loaded.identity, issued.identity);
        assert_eq!(loaded.certificate, issued.certificate);
        assert_eq!(loaded.intermediates, issued.intermediates);
        assert_eq!(loaded.trust_anchors, issued.trust_anchors);
        assert_eq!(loaded.not_after, issued.not_after);
        assert_eq!(
            loaded.issued_at,
            Some(UNIX_EPOCH + Duration::from_hours(498_000))
        );
        let pem = fs::read_to_string(layout.certificate_file()).unwrap();
        assert_eq!(pem.matches("BEGIN CERTIFICATE").count(), 2);
        assert!(!pem.contains('\r'));
    }

    #[test]
    fn converts_unix_seconds_without_panicking() {
        assert_eq!(from_unix(0), Some(UNIX_EPOCH));
        assert_eq!(from_unix(60), Some(UNIX_EPOCH + Duration::from_mins(1)));
        // A tampered identity.toml must not crash the agent (no panics on untrusted input).
        assert_eq!(from_unix(u64::MAX), None);
    }

    #[test]
    fn replaces_an_existing_identity_as_a_whole_and_leaves_no_staging_behind() {
        let dir = tempfile::tempdir().unwrap();
        let layout = StateLayout::new(dir.path());
        let old_key = AgentKey::generate().unwrap();
        store(&layout, &old_key, &issued_for(&old_key), UNIX_EPOCH).unwrap();
        let new_key = AgentKey::generate().unwrap();

        store(&layout, &new_key, &issued_for(&new_key), UNIX_EPOCH).unwrap();

        let loaded = load(&layout).unwrap();
        assert_eq!(loaded.key.public_key_info(), new_key.public_key_info());
        let names: Vec<String> = fs::read_dir(dir.path())
            .unwrap()
            .map(|entry| entry.unwrap().file_name().to_string_lossy().into_owned())
            .collect();
        assert_eq!(names, vec!["identity"]);
    }

    #[test]
    fn refuses_a_key_readable_by_others() {
        let dir = tempfile::tempdir().unwrap();
        let layout = StateLayout::new(dir.path());
        let key = AgentKey::generate().unwrap();
        store(&layout, &key, &issued_for(&key), UNIX_EPOCH).unwrap();
        fs::set_permissions(layout.key_file(), fs::Permissions::from_mode(0o640)).unwrap();

        let error = load(&layout).unwrap_err();

        assert!(error.to_string().contains("chmod 600"), "{error}");
    }

    #[test]
    fn refuses_a_certificate_of_another_key() {
        let dir = tempfile::tempdir().unwrap();
        let layout = StateLayout::new(dir.path());
        let key = AgentKey::generate().unwrap();
        let other = AgentKey::generate().unwrap();
        store(&layout, &key, &issued_for(&other), UNIX_EPOCH).unwrap();

        let error = load(&layout).unwrap_err();

        assert!(
            error.to_string().contains("does not belong to agent.key"),
            "{error}"
        );
    }

    #[test]
    fn reports_missing_and_corrupt_files_with_their_path() {
        let dir = tempfile::tempdir().unwrap();
        let layout = StateLayout::new(dir.path());
        assert!(!is_enrolled(&layout));
        assert!(load(&layout).unwrap_err().to_string().contains("agent.key"));

        let key = AgentKey::generate().unwrap();
        store(&layout, &key, &issued_for(&key), UNIX_EPOCH).unwrap();
        fs::write(layout.identity_file(), "node_id = 1\n").unwrap();
        assert!(
            load(&layout)
                .unwrap_err()
                .to_string()
                .contains("identity.toml")
        );
    }
}

#[cfg(test)]
mod trust_bundle_tests {
    use super::*;

    #[test]
    fn replaces_the_trust_bundle_and_keeps_the_rest() {
        let dir = tempfile::tempdir().unwrap();
        let layout = StateLayout::new(dir.path());
        let key = AgentKey::generate().unwrap();
        let root = rcgen::CertificateParams::new(Vec::<String>::new())
            .unwrap()
            .self_signed(&rcgen::KeyPair::generate().unwrap())
            .unwrap()
            .der()
            .clone();
        let certificate = rcgen::CertificateParams::new(Vec::<String>::new())
            .unwrap()
            .self_signed(&rcgen::KeyPair::from_pem(&key.to_pem()).unwrap())
            .unwrap()
            .der()
            .clone();
        let issued = IssuedCertificate {
            identity: AgentIdentity {
                org_id: "0192f6c8-1111-7000-8000-000000000001".into(),
                node_id: "0192f6c8-2222-7000-8000-000000000002".into(),
            },
            certificate,
            intermediates: vec![],
            trust_anchors: vec![root.clone()],
            not_after: UNIX_EPOCH + Duration::from_hours(499_344),
        };
        store(&layout, &key, &issued, UNIX_EPOCH).unwrap();
        let next = rcgen::CertificateParams::new(Vec::<String>::new())
            .unwrap()
            .self_signed(&rcgen::KeyPair::generate().unwrap())
            .unwrap()
            .der()
            .clone();

        store_trust_bundle(&layout, &[root.clone(), next.clone()]).unwrap();

        let loaded = load(&layout).unwrap();
        assert_eq!(loaded.trust_anchors, vec![root, next]);
        assert_eq!(loaded.key.public_key_info(), key.public_key_info());
        let names: Vec<String> = fs::read_dir(layout.identity_dir())
            .unwrap()
            .map(|entry| entry.unwrap().file_name().to_string_lossy().into_owned())
            .filter(|name| name.starts_with('.'))
            .collect();
        assert_eq!(names, Vec::<String>::new());
    }
}
