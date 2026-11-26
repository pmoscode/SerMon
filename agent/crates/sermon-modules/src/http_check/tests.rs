use std::net::SocketAddr;
use std::sync::Arc;
use std::time::SystemTime;

use sermon_core::Value;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpListener;

use super::*;
use crate::veto::Net;
use crate::{Schema, Secrets, Veto};

fn build(config: &str, veto: &Veto) -> Result<Box<dyn Instance>, BuildError> {
    let config = Schema::parse(SCHEMA)
        .unwrap()
        .validate(config, &[])
        .unwrap();
    HttpCheck.build(
        &config,
        &Setup {
            veto,
            secrets: &Secrets::default(),
            host: &crate::Host::default(),
        },
    )
}

/// A server answering every request with `status`.
async fn serve(status: &'static str) -> SocketAddr {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    tokio::spawn(async move {
        while let Ok((mut s, _)) = listener.accept().await {
            tokio::spawn(async move {
                let mut buf = [0u8; 4096];
                let _ = s.read(&mut buf).await;
                let answer = format!("HTTP/1.1 {status}\r\nContent-Length: 2\r\n\r\nok");
                let _ = s.write_all(answer.as_bytes()).await;
            });
        }
    });
    addr
}

fn value(samples: &[Sample], metric: &str, target: &str) -> Option<Value> {
    samples
        .iter()
        .find(|s| s.metric == metric && s.labels.iter().any(|(_, v)| v == target))
        .map(|s| s.value)
}

#[tokio::test]
async fn reports_up_status_and_latency() {
    let ok = serve("200 OK").await;
    let broken = serve("503 Service Unavailable").await;
    let bad = serve("400 Bad Request").await;
    let closed = TcpListener::bind("127.0.0.1:0")
        .await
        .unwrap()
        .local_addr()
        .unwrap();
    let (up, down, gone) = (
        format!("http://{ok}/health"),
        format!("http://{broken}/"),
        format!("http://{closed}/"),
    );
    let bad_request = format!("http://{bad}/");
    let mut check = build(
        &format!(r#"{{"urls":["{up}?token=x","{down}","{gone}","{bad_request}"],"timeout":"2s"}}"#),
        &Veto::default(),
    )
    .unwrap();
    assert_eq!(check.timeout(), Duration::from_secs(4));
    let samples = check.collect(&Run::default()).await.unwrap();
    assert_eq!(
        value(&samples, "sermon_http_check_up", &up),
        Some(Value::Int(1)),
        "the label has no query"
    );
    assert_eq!(
        value(&samples, "sermon_http_check_status_code", &up),
        Some(Value::Int(200))
    );
    assert!(matches!(
        value(&samples, "sermon_http_check_duration_seconds", &up),
        Some(Value::Float(s)) if (0.0..2.0).contains(&s)
    ));
    assert_eq!(
        value(&samples, "sermon_http_check_up", &down),
        Some(Value::Int(0))
    );
    assert_eq!(
        value(&samples, "sermon_http_check_status_code", &down),
        Some(Value::Int(503))
    );
    assert_eq!(
        value(&samples, "sermon_http_check_up", &gone),
        Some(Value::Int(0))
    );
    assert_eq!(
        value(&samples, "sermon_http_check_status_code", &gone),
        None,
        "no answer"
    );
    assert_eq!(
        value(&samples, "sermon_http_check_cert_expires_in_seconds", &up),
        None,
        "http"
    );
    assert_eq!(
        value(&samples, "sermon_http_check_up", &bad_request),
        Some(Value::Int(0)),
        "400"
    );
    assert_eq!(samples.len(), 10);

    let mut expect_503 = build(
        &format!(r#"{{"urls":["{down}"],"expect_status":[503]}}"#),
        &Veto::default(),
    )
    .unwrap();
    let samples = expect_503.collect(&Run::default()).await.unwrap();
    assert_eq!(
        value(&samples, "sermon_http_check_up", &down),
        Some(Value::Int(1))
    );
}

// ADR 0039: an endpoint outside the veto is not asked; the module says why.
#[tokio::test]
async fn refuses_endpoints_outside_the_veto() {
    let ok = serve("200 OK").await;
    let veto = Veto {
        targets: vec![Net::parse("127.0.0.2/32").unwrap()],
        ..Veto::default()
    };
    let mut check = build(&format!(r#"{{"urls":["http://{ok}/"]}}"#), &veto).unwrap();
    assert!(matches!(
        check.collect(&Run::default()).await,
        Err(RunError::Vetoed(_))
    ));
}

#[tokio::test]
async fn reports_the_time_a_certificate_has_left() {
    let mut params = rcgen::CertificateParams::new(vec!["localhost".to_owned()]).unwrap();
    params.not_after = rcgen::date_time_ymd(2040, 1, 1);
    let key = rcgen::KeyPair::generate().unwrap();
    let cert = params.self_signed(&key).unwrap();
    let config = rustls::ServerConfig::builder_with_provider(Arc::new(
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
    let acceptor = tokio_rustls::TlsAcceptor::from(Arc::new(config));
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let port = listener.local_addr().unwrap().port();
    tokio::spawn(async move {
        while let Ok((s, _)) = listener.accept().await {
            let acceptor = acceptor.clone();
            tokio::spawn(async move {
                let Ok(mut s) = acceptor.accept(s).await else {
                    return;
                };
                let mut buf = [0u8; 4096];
                let _ = s.read(&mut buf).await;
                let _ = s
                    .write_all(b"HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n")
                    .await;
                let _ = s.shutdown().await;
            });
        }
    });
    let url = format!("https://localhost:{port}/");
    let mut unverified = build(
        &format!(r#"{{"urls":["{url}"],"tls_verify":false}}"#),
        &Veto::default(),
    )
    .unwrap();
    // A run on 2039-12-01: 31 days left of the certificate's 2040-01-01.
    let expiry: u64 = 2_208_988_800;
    let clock = sermon_core::testing::FakeClock::new(
        SystemTime::UNIX_EPOCH + Duration::from_secs(expiry) - Duration::from_hours(31 * 24),
    );
    let samples = unverified
        .collect(&Run::new(Arc::new(clock)))
        .await
        .unwrap();
    assert_eq!(
        value(&samples, "sermon_http_check_up", &url),
        Some(Value::Int(1))
    );
    assert_eq!(
        value(&samples, "sermon_http_check_cert_expires_in_seconds", &url),
        Some(Value::Int(31 * 86_400))
    );
    let mut verified = build(&format!(r#"{{"urls":["{url}"]}}"#), &Veto::default()).unwrap();
    let samples = verified.collect(&Run::default()).await.unwrap();
    assert_eq!(
        value(&samples, "sermon_http_check_up", &url),
        Some(Value::Int(0)),
        "self-signed"
    );
}

#[test]
fn checks_the_configuration() {
    let veto = Veto::default();
    for bad in [
        r#"{"urls":[]}"#,
        r#"{"urls":["http://localhost/a","http://localhost/a?b"]}"#,
        r#"{"urls":["http://localhost/"],"timeout":"30s"}"#,
        r#"{"urls":["http://localhost/"],"timeout":"0ms"}"#,
    ] {
        assert!(
            matches!(build(bad, &veto), Err(BuildError::Invalid(_))),
            "{bad}"
        );
    }
    let outside = build(
        r#"{"urls":["http://localhost/"],"ca_file":"/etc/shadow"}"#,
        &veto,
    );
    assert!(matches!(outside, Err(BuildError::Vetoed(_))));
    let schema = Schema::parse(SCHEMA).unwrap();
    assert!(
        schema
            .validate(
                &format!(
                    r#"{{"urls":["{}"]}}"#,
                    ["http://alice", "x@localhost/"].join(":")
                ),
                &[]
            )
            .is_err()
    );
    assert!(
        schema
            .validate(r#"{"urls":["ftp://localhost/"]}"#, &[])
            .is_err()
    );
    let config = schema
        .validate(r#"{"urls":["http://localhost/"]}"#, &[])
        .unwrap();
    assert_eq!(
        config["tls_verify"],
        serde_json::json!(true),
        "verified by default"
    );
}
