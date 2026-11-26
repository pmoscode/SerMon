use std::sync::Arc;

use serde_json::json;
use tokio::net::{TcpListener, UnixListener};

use super::*;

fn config(value: &Value) -> Config {
    value.as_object().cloned().unwrap_or_default()
}

fn tcp(host: &str) -> Target {
    Target::Tcp {
        host: host.to_owned(),
        port: 5432,
    }
}

fn tls_of(c: &Value, target: &Target) -> Result<Tls, BuildError> {
    Security::of(&config(c), target, &Veto::default()).map(|s| s.tls)
}

// M5-33: without a setting TLS is off only on this host; a database elsewhere is verified.
#[test]
fn tls_is_verified_unless_the_target_is_this_host() {
    let unix = Target::Unix("/run/postgresql/.s.PGSQL.5432".into());
    for (target, want) in [
        (tcp("127.0.0.1"), Tls::Off),
        (tcp("127.0.0.53"), Tls::Off),
        (tcp("::1"), Tls::Off),
        (tcp("localhost"), Tls::Off),
        (tcp("LocalHost"), Tls::Off),
        (unix.clone(), Tls::Off),
        (tcp("192.0.2.10"), Tls::Verify),
        (tcp("2001:db8::7"), Tls::Verify),
        (tcp("db.example"), Tls::Verify),
        (tcp("localhost.example"), Tls::Verify),
    ] {
        assert_eq!(tls_of(&json!({}), &target).unwrap(), want, "{target:?}");
    }
    let far = tcp("192.0.2.10");
    assert_eq!(tls_of(&json!({"tls": "off"}), &far).unwrap(), Tls::Off);
    assert_eq!(
        tls_of(&json!({"tls": "no_verify"}), &far).unwrap(),
        Tls::NoVerify
    );
    assert_eq!(
        tls_of(&json!({"tls": "verify"}), &tcp("127.0.0.1")).unwrap(),
        Tls::Verify
    );
    assert!(matches!(
        tls_of(&json!({"tls": "verify"}), &unix),
        Err(BuildError::Invalid(_))
    ));
}

#[test]
fn a_ca_file_only_with_verify_and_inside_the_veto() {
    let dir = crate::TestDir::new();
    let pem = dir.path().join("ca.pem");
    let key = rcgen::KeyPair::generate().unwrap();
    let cert = rcgen::CertificateParams::new(vec!["localhost".to_owned()])
        .unwrap()
        .self_signed(&key)
        .unwrap();
    std::fs::write(&pem, cert.pem()).unwrap();
    std::fs::write(dir.path().join("empty.pem"), "").unwrap();
    let veto = Veto {
        file_prefixes: vec![dir.path().to_path_buf()],
        ..Veto::default()
    };
    let far = tcp("192.0.2.10");
    let of = |c: Value, veto: &Veto| Security::of(&config(&c), &far, veto);
    let path = pem.display().to_string();
    assert!(of(json!({ "ca_file": path }), &veto).is_ok());
    assert!(of(json!({ "tls": "verify", "ca_file": path }), &veto).is_ok());
    for tls in ["off", "no_verify"] {
        assert!(matches!(
            of(json!({ "tls": tls, "ca_file": path }), &veto),
            Err(BuildError::Invalid(_))
        ));
    }
    assert!(matches!(
        of(json!({ "ca_file": path }), &Veto::default()),
        Err(BuildError::Vetoed(_))
    ));
    let empty = dir.path().join("empty.pem").display().to_string();
    assert!(matches!(
        of(json!({ "ca_file": empty }), &veto),
        Err(BuildError::Invalid(_))
    ));
}

/// A TCP connection to a listener of this host, and the listener's side.
async fn connected() -> (Connection, TcpStream) {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let at = listener.local_addr().unwrap();
    let (client, server) = tokio::join!(TcpStream::connect(at), listener.accept());
    (
        Connection::Tcp(client.unwrap(), "localhost".into()),
        server.unwrap().0,
    )
}

// M5-33: only the socket and verified TLS count as the database itself; no_verify encrypts to
// whoever answers.
#[tokio::test]
async fn only_the_socket_and_verified_tls_are_secure() {
    let at = |tls| Security { tls, roots: None };
    let (connection, _server) = connected().await;
    assert!(at(Tls::Verify).secure(&connection));
    assert!(!at(Tls::NoVerify).secure(&connection));
    assert!(!at(Tls::Off).secure(&connection));
    let dir = crate::TestDir::new();
    let path = dir.path().join("db.sock");
    let _listener = UnixListener::bind(&path).unwrap();
    let unix = Connection::Unix(UnixStream::connect(&path).await.unwrap());
    assert!(at(Tls::Off).secure(&unix));
}

// M5-33: a socket that turns into a link out of module_sockets after the build is refused at
// the connection.
#[tokio::test]
async fn checks_the_socket_at_every_connection() {
    let (allowed, outside) = (crate::TestDir::new(), crate::TestDir::new());
    let real = outside.path().join("real.sock");
    let _listener = UnixListener::bind(&real).unwrap();
    let veto = Veto {
        sockets: vec![allowed.path().to_path_buf()],
        ..Veto::default()
    };
    let path = allowed.path().join("db.sock");
    let config = serde_json::json!({ "socket": path });
    let target = Target::of(config.as_object().unwrap(), &veto).unwrap();
    assert_eq!(target, Target::Unix(path.clone()));
    std::os::unix::fs::symlink(&real, &path).unwrap();
    assert!(matches!(
        target.connect(&veto).await,
        Err(RunError::Vetoed(_))
    ));
    std::fs::remove_file(&path).unwrap();
    let _inside = UnixListener::bind(&path).unwrap();
    assert!(target.connect(&veto).await.is_ok());
}

// The CA file is what the certificate is verified with.
#[tokio::test]
async fn verifies_with_the_ca_file() {
    let key = rcgen::KeyPair::generate().unwrap();
    let cert = rcgen::CertificateParams::new(vec!["localhost".to_owned()])
        .unwrap()
        .self_signed(&key)
        .unwrap();
    let mut roots = RootCertStore::empty();
    roots.add(cert.der().clone()).unwrap();
    let server = rustls::ServerConfig::builder_with_provider(Arc::new(
        rustls::crypto::ring::default_provider(),
    ))
    .with_safe_default_protocol_versions()
    .unwrap()
    .with_no_client_auth()
    .with_single_cert(
        vec![cert.der().clone()],
        rustls::pki_types::PrivateKeyDer::Pkcs8(key.serialize_der().into()),
    )
    .unwrap();
    let acceptor = tokio_rustls::TlsAcceptor::from(Arc::new(server));
    let handshake = async |security: Security| {
        let (Connection::Tcp(client, host), server) = connected().await else {
            unreachable!()
        };
        let accept = acceptor.accept(server);
        let (client, _) = tokio::join!(security.start(client, &host), accept);
        client.is_ok()
    };
    let own = Security {
        tls: Tls::Verify,
        roots: Some(Arc::new(roots)),
    };
    assert!(handshake(own).await, "verified with the CA file");
    let system = Security {
        tls: Tls::Verify,
        roots: None,
    };
    assert!(!handshake(system).await, "not by the system's CAs");
}
