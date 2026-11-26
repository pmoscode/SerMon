use std::path::{Path, PathBuf};
use std::time::SystemTime;

use sermon_core::Value;
use sermon_core::testing::FakeClock;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpListener;

use super::*;
use crate::veto::Net;
use crate::{Host, Schema, Secrets};

/// 2026-06-01, while the fixture leaf (2026-01-01 to 2027-01-01) is valid.
const JUNE_2026: u64 = 1_780_272_000;
/// 2027-01-01, the end of the fixture leaf.
const LEAF_END: i64 = 1_798_761_600;
/// 2025-06-01, the end of the expired fixture leaf.
const EXPIRED_END: i64 = 1_748_736_000;

fn fixtures() -> PathBuf {
    std::fs::canonicalize(
        Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures/modules/tls/certs"),
    )
    .unwrap()
}

fn veto_for(dirs: &[&Path]) -> Veto {
    Veto {
        file_prefixes: dirs
            .iter()
            .map(|d| std::fs::canonicalize(d).unwrap())
            .collect(),
        ..Veto::default().any_port()
    }
}

fn build(config: &str, veto: &Veto) -> Result<Box<dyn Instance>, BuildError> {
    let config = Schema::parse(SCHEMA)
        .unwrap()
        .validate(config, &[])
        .unwrap();
    Tls.build(
        &config,
        &Setup {
            veto,
            secrets: &Secrets::default(),
            host: &Host::default(),
        },
    )
}

fn at(seconds: u64) -> Run {
    Run::new(Arc::new(FakeClock::new(
        SystemTime::UNIX_EPOCH + Duration::from_secs(seconds),
    )))
}

fn value(samples: &[Sample], metric: &str, target: &str) -> Option<Value> {
    samples
        .iter()
        .find(|s| s.metric == metric && s.labels.iter().any(|(_, v)| v == target))
        .map(|s| s.value)
}

fn file(name: &str) -> String {
    fixtures().join(name).to_string_lossy().into_owned()
}

// The plan's fixtures: a chain, a leaf without its intermediate, an expired leaf, a foreign
// name and a self-signed certificate, checked against the fixture root.
#[tokio::test]
async fn judges_certificate_files() {
    let dir = fixtures();
    let names = [
        "fullchain.crt",
        "leaf-only.crt",
        "expired.crt",
        "foreign.crt",
        "selfsigned.crt",
        "missing.crt",
    ];
    let files: Vec<String> = names.iter().map(|n| format!("\"{}\"", file(n))).collect();
    let config = format!(
        r#"{{"files":[{}],"ca_file":"{}"}}"#,
        files.join(","),
        file("root.crt")
    );
    let mut tls = build(&config, &veto_for(&[&dir])).unwrap();
    let s = tls.collect(&at(JUNE_2026)).await.unwrap();
    crate::assert_in_catalog("tls", &s);
    let june = i64::try_from(JUNE_2026).unwrap();
    let full = file("fullchain.crt");
    assert_eq!(value(&s, "sermon_tls_up", &full), Some(Value::Int(1)));
    assert_eq!(
        value(&s, "sermon_tls_cert_expires_in_seconds", &full),
        Some(Value::Int(LEAF_END - june))
    );
    assert_eq!(
        value(&s, "sermon_tls_chain_expires_in_seconds", &full),
        Some(Value::Int(LEAF_END - june)),
        "the intermediate lasts longer"
    );
    assert_eq!(
        value(&s, "sermon_tls_chain_valid", &full),
        Some(Value::Int(1))
    );
    assert_eq!(
        value(&s, "sermon_tls_name_valid", &full),
        None,
        "no name for files"
    );

    let leaf_only = file("leaf-only.crt");
    assert_eq!(
        value(&s, "sermon_tls_chain_valid", &leaf_only),
        Some(Value::Int(0)),
        "no intermediate"
    );
    let expired = file("expired.crt");
    assert_eq!(
        value(&s, "sermon_tls_cert_expires_in_seconds", &expired),
        Some(Value::Int(EXPIRED_END - june))
    );
    assert_eq!(
        value(&s, "sermon_tls_chain_valid", &expired),
        Some(Value::Int(1)),
        "expiry is its own metric"
    );
    assert_eq!(
        value(&s, "sermon_tls_chain_valid", &file("foreign.crt")),
        Some(Value::Int(1))
    );
    assert_eq!(
        value(&s, "sermon_tls_chain_valid", &file("selfsigned.crt")),
        Some(Value::Int(0)),
        "another issuer"
    );
    let missing = file("missing.crt");
    assert_eq!(value(&s, "sermon_tls_up", &missing), Some(Value::Int(0)));
    assert_eq!(
        value(&s, "sermon_tls_cert_expires_in_seconds", &missing),
        None
    );
    assert_eq!(s.len(), 5 * 4 + 1);
}

// ADR 0039: files only below module_file_prefixes – also the CA file.
#[test]
fn refuses_files_outside_the_prefixes() {
    let dir = fixtures();
    let full = file("fullchain.crt");
    assert!(matches!(
        build(
            &format!(r#"{{"files":["{full}"]}}"#),
            &Veto::default().any_port()
        ),
        Err(BuildError::Vetoed(_))
    ));
    let other = crate::TestDir::new();
    std::fs::copy(fixtures().join("root.crt"), other.path().join("root.crt")).unwrap();
    let outside_ca = other.path().join("root.crt");
    assert!(
        matches!(
            build(
                &format!(
                    r#"{{"files":["{full}"],"ca_file":"{}"}}"#,
                    outside_ca.display()
                ),
                &veto_for(&[&dir])
            ),
            Err(BuildError::Vetoed(_))
        ),
        "the CA file too"
    );
    assert!(matches!(
        build(
            r#"{"files":["/etc/sermon/agent.toml"]}"#,
            &veto_for(&[Path::new("/")])
        ),
        Err(BuildError::Vetoed(_))
    ));
    assert!(matches!(
        build("{}", &veto_for(&[&dir])),
        Err(BuildError::Invalid(_))
    ));
}

// A file that turns into a link out of the prefixes after the start is refused at the run.
#[tokio::test]
async fn checks_the_veto_at_every_run() {
    let allowed = crate::TestDir::new();
    let outside = crate::TestDir::new();
    let path = allowed.path().join("cert.pem");
    std::fs::copy(fixtures().join("fullchain.crt"), &path).unwrap();
    let mut tls = build(
        &format!(r#"{{"files":["{}"]}}"#, path.display()),
        &veto_for(&[allowed.path()]),
    )
    .unwrap();
    assert!(tls.collect(&at(JUNE_2026)).await.is_ok());
    std::fs::copy(
        fixtures().join("fullchain.crt"),
        outside.path().join("c.pem"),
    )
    .unwrap();
    std::fs::remove_file(&path).unwrap();
    std::os::unix::fs::symlink(outside.path().join("c.pem"), &path).unwrap();
    assert!(matches!(
        tls.collect(&at(JUNE_2026)).await,
        Err(RunError::Vetoed(_))
    ));
}

// A combined file (haproxy): the key is skipped, nothing of it is kept.
#[tokio::test]
async fn reads_only_the_certificates_of_a_file() {
    let dir = crate::TestDir::new();
    let key = rcgen::KeyPair::generate().unwrap();
    let chain = std::fs::read_to_string(fixtures().join("fullchain.crt")).unwrap();
    let path = dir.path().join("combined.pem");
    std::fs::write(&path, format!("{}{chain}", key.serialize_pem())).unwrap();
    let certs = read_file(&veto_for(&[dir.path()]), &path).ok().unwrap();
    assert_eq!(certs.len(), 2);
    std::fs::write(&path, vec![b'x'; usize::try_from(MAX_FILE).unwrap() + 1]).unwrap();
    assert!(matches!(
        read_file(&veto_for(&[dir.path()]), &path),
        Err(Unread::Unreadable)
    ));
}

/// A TLS server on loopback with a leaf for `names` signed by a fresh CA; the CA's PEM.
async fn endpoint(names: &[&str]) -> (u16, String) {
    let ca_key = rcgen::KeyPair::generate().unwrap();
    let mut ca = rcgen::CertificateParams::new(Vec::<String>::new()).unwrap();
    ca.is_ca = rcgen::IsCa::Ca(rcgen::BasicConstraints::Unconstrained);
    let ca_cert = ca.self_signed(&ca_key).unwrap();
    let issuer = rcgen::Issuer::new(ca, ca_key);
    let key = rcgen::KeyPair::generate().unwrap();
    let mut leaf =
        rcgen::CertificateParams::new(names.iter().map(|n| (*n).to_owned()).collect::<Vec<_>>())
            .unwrap();
    leaf.not_after = rcgen::date_time_ymd(2040, 1, 1);
    let leaf = leaf.signed_by(&key, &issuer).unwrap();
    let config = rustls::ServerConfig::builder_with_provider(Arc::new(
        rustls::crypto::ring::default_provider(),
    ))
    .with_safe_default_protocol_versions()
    .unwrap()
    .with_no_client_auth()
    .with_single_cert(
        vec![leaf.der().clone()],
        rustls::pki_types::PrivateKeyDer::Pkcs8(key.serialize_der().into()),
    )
    .unwrap();
    let acceptor = tokio_rustls::TlsAcceptor::from(Arc::new(config));
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let port = listener.local_addr().unwrap().port();
    tokio::spawn(async move {
        while let Ok((s, _)) = listener.accept().await {
            let acceptor = acceptor.clone();
            tokio::spawn(async move {
                if let Ok(mut s) = acceptor.accept(s).await {
                    let mut buf = [0u8; 64];
                    let _ = s.read(&mut buf).await;
                    let _ = s.shutdown().await;
                }
            });
        }
    });
    (port, ca_cert.pem())
}

#[tokio::test]
async fn judges_endpoints_and_their_names() {
    let (port, ca) = endpoint(&["localhost"]).await;
    let dir = crate::TestDir::new();
    std::fs::write(dir.path().join("ca.pem"), ca).unwrap();
    let ca_file = std::fs::canonicalize(dir.path().join("ca.pem")).unwrap();
    let veto = veto_for(&[dir.path()]);
    let (by_name, by_ip) = (format!("localhost:{port}"), format!("127.0.0.1:{port}"));
    let closed = TcpListener::bind("127.0.0.1:0")
        .await
        .unwrap()
        .local_addr()
        .unwrap()
        .to_string();
    let mut tls = build(
        &format!(
            r#"{{"endpoints":["{by_name}","{by_ip}","{closed}"],"ca_file":"{}"}}"#,
            ca_file.display()
        ),
        &veto,
    )
    .unwrap();
    let s = tls.collect(&at(JUNE_2026)).await.unwrap();
    crate::assert_in_catalog("tls", &s);
    assert_eq!(value(&s, "sermon_tls_up", &by_name), Some(Value::Int(1)));
    assert_eq!(
        value(&s, "sermon_tls_chain_valid", &by_name),
        Some(Value::Int(1))
    );
    assert_eq!(
        value(&s, "sermon_tls_name_valid", &by_name),
        Some(Value::Int(1))
    );
    assert_eq!(
        value(&s, "sermon_tls_name_valid", &by_ip),
        Some(Value::Int(0)),
        "the certificate has no IP"
    );
    assert_eq!(value(&s, "sermon_tls_up", &closed), Some(Value::Int(0)));
    let mut other = build(
        &format!(
            r#"{{"endpoints":["{by_ip}"],"server_name":"other.example","ca_file":"{}"}}"#,
            ca_file.display()
        ),
        &veto,
    )
    .unwrap();
    let s = other.collect(&at(JUNE_2026)).await.unwrap();
    let mut named = build(
        &format!(
            r#"{{"endpoints":["{by_ip}"],"server_name":"localhost","ca_file":"{}"}}"#,
            ca_file.display()
        ),
        &veto,
    )
    .unwrap();
    let n = named.collect(&at(JUNE_2026)).await.unwrap();
    assert_eq!(
        value(&n, "sermon_tls_name_valid", &by_ip),
        Some(Value::Int(1)),
        "by the server name"
    );
    assert_eq!(
        value(&s, "sermon_tls_name_valid", &by_ip),
        Some(Value::Int(0)),
        "a foreign name"
    );
    assert_eq!(
        value(&s, "sermon_tls_chain_valid", &by_ip),
        Some(Value::Int(1))
    );

    let only_other = Veto {
        targets: vec![Net::parse("127.0.0.2/32").unwrap()],
        ..veto
    };
    let mut refused = build(&format!(r#"{{"endpoints":["{by_ip}"]}}"#), &only_other).unwrap();
    assert!(matches!(
        refused.collect(&at(JUNE_2026)).await,
        Err(RunError::Vetoed(_))
    ));
}

#[test]
fn judges_nothing_of_no_certificate() {
    assert_eq!(judge(&[], 0, None, None), Verdict::default());
    let junk = CertificateDer::from(vec![0x30, 0x03, 0x02, 0x01, 0x01]);
    assert_eq!(judge(&[junk], 0, None, None), Verdict::default());
}
