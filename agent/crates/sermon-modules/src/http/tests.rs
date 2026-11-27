use std::sync::Mutex;

use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::{TcpListener, UnixListener};

use super::*;
use crate::veto::Net;

type Handler = Arc<dyn Fn(&str, SocketAddr) -> Vec<u8> + Send + Sync>;

/// A server on loopback that answers each connection with `handler(request, own address)`;
/// it keeps the requests.
struct Server {
    addr: SocketAddr,
    requests: Arc<Mutex<Vec<String>>>,
}

async fn read_request<S: AsyncRead + Unpin>(stream: &mut S) -> String {
    let mut buf = Vec::new();
    let mut chunk = [0u8; 1024];
    while !buf.windows(4).any(|w| w == b"\r\n\r\n") {
        match stream.read(&mut chunk).await {
            Ok(0) | Err(_) => break,
            Ok(n) => buf.extend_from_slice(&chunk[..n]),
        }
    }
    String::from_utf8_lossy(&buf).into_owned()
}

async fn respond<S: AsyncRead + AsyncWrite + Unpin>(
    mut stream: S,
    handler: &Handler,
    seen: &Mutex<Vec<String>>,
    addr: SocketAddr,
) {
    let request = read_request(&mut stream).await;
    let answer = handler(&request, addr);
    seen.lock().unwrap().push(request);
    let _ = stream.write_all(&answer).await;
    let _ = stream.shutdown().await;
}

async fn serve(handler: Handler) -> Server {
    serve_at("127.0.0.1:0", handler, None).await
}

async fn serve_at(bind: &str, handler: Handler, tls: Option<tokio_rustls::TlsAcceptor>) -> Server {
    let listener = TcpListener::bind(bind).await.unwrap();
    let addr = listener.local_addr().unwrap();
    let requests = Arc::new(Mutex::new(Vec::new()));
    let seen = requests.clone();
    tokio::spawn(async move {
        loop {
            let Ok((stream, _)) = listener.accept().await else {
                return;
            };
            let (handler, seen, tls) = (handler.clone(), seen.clone(), tls.clone());
            tokio::spawn(async move {
                if let Some(acceptor) = tls {
                    if let Ok(stream) = acceptor.accept(stream).await {
                        respond(stream, &handler, &seen, addr).await;
                    }
                } else {
                    respond(stream, &handler, &seen, addr).await;
                }
            });
        }
    });
    Server { addr, requests }
}

fn answer(status: &str, headers: &str, body: &str) -> Vec<u8> {
    format!(
        "HTTP/1.1 {status}\r\n{headers}Content-Length: {}\r\nConnection: close\r\n\r\n{body}",
        body.len()
    )
    .into_bytes()
}

fn fixed(status: &'static str, headers: &'static str, body: &'static str) -> Handler {
    Arc::new(move |_, _| answer(status, headers, body))
}

fn url(s: &str) -> Url {
    Url::parse(s).unwrap()
}

fn client(veto: &Veto, options: Options) -> Client {
    Client::new(veto, options).unwrap()
}

#[test]
fn parses_urls() {
    let u = url("http://LocalHost:8080/nginx_status?x=1#top");
    assert_eq!((u.host(), u.port(), u.https()), ("localhost", 8080, false));
    assert_eq!(u.path, "/nginx_status?x=1");
    assert_eq!(
        u.label(),
        "http://localhost:8080/nginx_status",
        "without the query"
    );
    assert_eq!(url("https://[::1]/").authority(), "[::1]");
    assert_eq!(url("https://[::1]:8443?a").path, "/?a");
    assert_eq!(url("https://127.0.0.1").port(), 443);
    // User and password, built at run time (no credential URL in the source).
    let with_password = ["http://alice", "x@localhost/"].join(":");
    let e = Url::parse(&with_password).unwrap_err();
    assert!(e.contains("without user and password"), "{e}");
    for bad in [
        "ftp://localhost/",
        "http://alice@localhost/",
        "http://@localhost/",
        "http:///path",
        "http://localhost:0/",
        "http://localhost:65536/",
        "http://localhost:x/",
        "http://[::1/",
        "http://[localhost]/",
        "http://[::1]x/",
        "http://local host/",
        "http://localhost/a b",
        "http://local_host/",
    ] {
        assert!(Url::parse(bad).is_err(), "{bad}");
    }
    assert!(Url::parse(&format!("http://localhost/{}", "a".repeat(MAX_URL))).is_err());
}

#[test]
fn joins_redirect_targets() {
    let base = url("https://localhost:8443/a/b?q");
    assert_eq!(base.join("/c").unwrap(), url("https://localhost:8443/c"));
    assert_eq!(
        base.join("c?d#e").unwrap(),
        url("https://localhost:8443/a/c?d")
    );
    assert_eq!(base.join("//other/x").unwrap(), url("https://other/x"));
    assert_eq!(
        base.join("http://192.0.2.1/").unwrap(),
        url("http://192.0.2.1/")
    );
    assert!(base.join("javascript:alert(1)").is_err());
    assert_eq!(
        url("http://[::1]:81").join("x").unwrap(),
        url("http://[::1]:81/x")
    );
}

#[tokio::test]
async fn gets_status_and_body() {
    let server = serve(fixed(
        "200 OK",
        "Content-Type: text/plain\r\n",
        "Active connections: 1 \n",
    ))
    .await;
    let c = client(&Veto::default().any_port(), Options::default());
    let r = c
        .get(&url(&format!("http://{}/nginx_status", server.addr)), true)
        .await
        .unwrap();
    assert_eq!(r.status, 200);
    assert_eq!(r.content_type.as_deref(), Some("text/plain"));
    assert_eq!(&r.body[..], b"Active connections: 1 \n");
    assert_eq!((r.redirects, r.certificate.is_none()), (0, true));
    let request = server.requests.lock().unwrap()[0].clone();
    assert!(
        request.starts_with("GET /nginx_status HTTP/1.1\r\n"),
        "{request}"
    );
    assert!(
        request
            .to_ascii_lowercase()
            .contains(&format!("host: {}", server.addr))
    );
    assert!(request.contains("sermon-agent/"));
    let r = c
        .get(&url(&format!("http://{}/", server.addr)), false)
        .await
        .unwrap();
    assert!(r.body.is_empty(), "no body asked for");
}

// ADR 0039: the client connects only to what the veto allows – also by IP literal.
#[tokio::test]
async fn connects_only_inside_the_veto() {
    let server = serve(fixed("200 OK", "", "")).await;
    let only_other = Veto {
        targets: vec![Net::parse("127.0.0.2/32").unwrap()],
        ..Veto::default().any_port()
    };
    let c = client(&only_other, Options::default());
    let e = c
        .get(&url(&format!("http://{}/", server.addr)), true)
        .await
        .unwrap_err();
    assert!(matches!(e, HttpError::Vetoed(_)), "{e}");
    assert!(
        server.requests.lock().unwrap().is_empty(),
        "nothing was sent"
    );
}

// The plan's test: a redirect out of the veto is refused, inside it is followed up to three
// times; the credential stays with its host.
#[tokio::test]
async fn follows_redirects_inside_the_veto_only() {
    let outside = serve(fixed(
        "302 Found",
        "Location: http://192.0.2.10/steal\r\n",
        "",
    ))
    .await;
    let c = client(&Veto::default().any_port(), Options::default());
    let e = c
        .get(&url(&format!("http://{}/", outside.addr)), true)
        .await
        .unwrap_err();
    assert!(matches!(e, HttpError::Vetoed(_)), "{e}");

    let hops: Handler = Arc::new(|request, _| {
        if request.starts_with("GET /loop ") {
            return answer("302 Found", "Location: /loop\r\n", "");
        }
        if request.starts_with("GET /four ") {
            return answer("302 Found", "Location: /hop0\r\n", "");
        }
        let n: u32 = request
            .split_whitespace()
            .nth(1)
            .and_then(|p| p.trim_start_matches("/hop").parse().ok())
            .unwrap_or(0);
        if n < 3 {
            answer("301 Moved", &format!("Location: /hop{}\r\n", n + 1), "")
        } else {
            answer("200 OK", "", "done")
        }
    });
    let server = serve(hops).await;
    let r = c
        .get(&url(&format!("http://{}/hop0", server.addr)), true)
        .await
        .unwrap();
    assert_eq!((r.status, &r.body[..], r.redirects), (200, &b"done"[..], 3));
    let e = c
        .get(&url(&format!("http://{}/loop", server.addr)), true)
        .await
        .unwrap_err();
    assert!(e.to_string().contains("more than 3 redirects"), "{e}");
    let e = c
        .get(&url(&format!("http://{}/four", server.addr)), true)
        .await
        .unwrap_err();
    assert!(e.to_string().contains("more than 3 redirects"), "four: {e}");

    let no_follow = client(
        &Veto::default().any_port(),
        Options {
            follow_redirects: false,
            ..Options::default()
        },
    );
    let r = no_follow
        .get(&url(&format!("http://{}/", outside.addr)), true)
        .await
        .unwrap();
    assert_eq!(r.status, 302, "the redirect is the answer");
}

#[tokio::test]
async fn the_credential_goes_to_its_host_only() {
    let target = serve(fixed("200 OK", "", "")).await;
    // The same host, another port: another service.
    let to = format!("http://127.0.0.1:{}/metrics", target.addr.port());
    let origin = serve(Arc::new(move |_, _| {
        answer("307 Redirect", &format!("Location: {to}\r\n"), "")
    }))
    .await;
    let c = client(
        &Veto::default().any_port(),
        Options {
            authorization: Some(String::from("Bearer t0ken").into()),
            ..Options::default()
        },
    );
    c.get(&url(&format!("http://{}/", origin.addr)), true)
        .await
        .unwrap();
    let first = origin.requests.lock().unwrap()[0].to_ascii_lowercase();
    assert!(first.contains("authorization: bearer t0ken"), "{first}");
    let second = target.requests.lock().unwrap()[0].to_ascii_lowercase();
    assert!(!second.contains("authorization"), "another host: {second}");
    assert!(!format!("{:?}", c.options).contains("t0ken"));
}

// The plan's tests: too large, too slow, too many headers.
#[tokio::test]
async fn bounds_the_answer() {
    let c = client(
        &Veto::default().any_port(),
        Options {
            timeout: Duration::from_millis(300),
            ..Options::default()
        },
    );
    let big = serve(Arc::new(|_, _| {
        answer("200 OK", "", &"x".repeat(MAX_BODY + 1))
    }))
    .await;
    let e = c
        .get(&url(&format!("http://{}/", big.addr)), true)
        .await
        .unwrap_err();
    assert!(
        matches!(&e, HttpError::Failed(why) if why.contains("larger than")),
        "{e}"
    );
    let exact = serve(Arc::new(|_, _| answer("200 OK", "", &"x".repeat(MAX_BODY)))).await;
    let r = c
        .get(&url(&format!("http://{}/", exact.addr)), true)
        .await
        .unwrap();
    assert_eq!(r.body.len(), MAX_BODY);

    let chunked = serve(Arc::new(|_, _| {
        let mut out = b"HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n".to_vec();
        for _ in 0..=MAX_BODY / 4096 {
            out.extend_from_slice(format!("1000\r\n{}\r\n", "y".repeat(4096)).as_bytes());
        }
        out.extend_from_slice(b"0\r\n\r\n");
        out
    }))
    .await;
    let e = c
        .get(&url(&format!("http://{}/", chunked.addr)), true)
        .await
        .unwrap_err();
    assert!(
        e.to_string().contains("larger than"),
        "without a length too: {e}"
    );

    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let silent = listener.local_addr().unwrap();
    tokio::spawn(async move {
        let mut open = Vec::new();
        while let Ok((s, _)) = listener.accept().await {
            open.push(s);
        }
    });
    let e = c
        .get(&url(&format!("http://{silent}/")), true)
        .await
        .unwrap_err();
    assert!(
        matches!(&e, HttpError::Unreachable(why) if why.contains("no answer within 300 ms")),
        "{e}"
    );
    let announced = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let liar = announced.local_addr().unwrap();
    tokio::spawn(async move {
        let mut open = Vec::new();
        while let Ok((mut s, _)) = announced.accept().await {
            let _ = read_request(&mut s).await;
            let head = format!(
                "HTTP/1.1 200 OK\r\nContent-Length: {}\r\n\r\nx",
                MAX_BODY + 1
            );
            let _ = s.write_all(head.as_bytes()).await;
            open.push(s);
        }
    });
    let e = c
        .get(&url(&format!("http://{liar}/")), true)
        .await
        .unwrap_err();
    assert!(
        e.to_string().contains("larger than"),
        "refused by its length: {e}"
    );
}

// Too many headers or no HTTP at all is a bad answer; a closed port is no answer.
#[tokio::test]
async fn tells_bad_answers_from_none() {
    let c = client(&Veto::default().any_port(), Options::default());
    let headers = serve(Arc::new(|_, _| {
        answer("200 OK", &"X-Filler: a\r\n".repeat(MAX_HEADERS + 1), "")
    }))
    .await;
    assert!(
        c.get(&url(&format!("http://{}/", headers.addr)), true)
            .await
            .is_err()
    );
    let garbage = serve(Arc::new(|_, _| b"SSH-2.0-OpenSSH_9.6\r\n".to_vec())).await;
    assert!(matches!(
        c.get(&url(&format!("http://{}/", garbage.addr)), true)
            .await,
        Err(HttpError::Failed(_))
    ));
    let closed = TcpListener::bind("127.0.0.1:0")
        .await
        .unwrap()
        .local_addr()
        .unwrap();
    assert!(matches!(
        c.get(&url(&format!("http://{closed}/")), true).await,
        Err(HttpError::Unreachable(_))
    ));
}

struct TlsServer {
    acceptor: tokio_rustls::TlsAcceptor,
    ca: crate::TestDir,
    ca_file: PathBuf,
}

fn tls(names: &[&str]) -> TlsServer {
    tls_until(names, 2040)
}

fn tls_until(names: &[&str], year: i32) -> TlsServer {
    let mut params =
        rcgen::CertificateParams::new(names.iter().map(|n| (*n).to_owned()).collect::<Vec<_>>())
            .unwrap();
    params.not_after = rcgen::date_time_ymd(year, 1, 1);
    let key = rcgen::KeyPair::generate().unwrap();
    let cert = params.self_signed(&key).unwrap();
    let ca = crate::TestDir::new();
    let ca_file = ca.path().join("ca.pem");
    std::fs::write(&ca_file, cert.pem()).unwrap();
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
    TlsServer {
        acceptor: tokio_rustls::TlsAcceptor::from(Arc::new(config)),
        ca,
        ca_file,
    }
}

// TLS is verified – against the CA file the veto allows – and off only explicitly.
#[tokio::test]
async fn verifies_tls() {
    let own = tls(&["localhost", "127.0.0.1"]);
    let server = serve_at(
        "127.0.0.1:0",
        fixed("200 OK", "", "secure"),
        Some(own.acceptor.clone()),
    )
    .await;
    let https = url(&format!("https://localhost:{}/", server.addr.port()));
    let veto = Veto {
        file_prefixes: vec![std::fs::canonicalize(own.ca.path()).unwrap()],
        ..Veto::default().any_port()
    };
    let with_ca = client(
        &veto,
        Options {
            ca_file: Some(own.ca_file.clone()),
            ..Options::default()
        },
    );
    let r = with_ca.get(&https, true).await.unwrap();
    assert_eq!(&r.body[..], b"secure");
    let validity = crate::x509::validity(r.certificate.as_ref().unwrap()).unwrap();
    assert_eq!(validity.not_after, 2_208_988_800, "2040-01-01");
    let by_ip = url(&format!("https://127.0.0.1:{}/", server.addr.port()));
    assert!(
        with_ca.get(&by_ip, true).await.is_ok(),
        "the IP is in the certificate"
    );

    let foreign = tls(&["other.example"]);
    let wrong_name = serve_at(
        "127.0.0.1:0",
        fixed("200 OK", "", ""),
        Some(foreign.acceptor),
    )
    .await;
    let e = with_ca
        .get(
            &url(&format!("https://localhost:{}/", wrong_name.addr.port())),
            true,
        )
        .await
        .unwrap_err();
    assert!(e.to_string().contains("TLS"), "an unknown CA: {e}");

    let unverified = client(
        &Veto::default().any_port(),
        Options {
            tls_verify: false,
            ..Options::default()
        },
    );
    let r = unverified.get(&https, true).await.unwrap();
    assert!(
        r.certificate.is_some(),
        "the expiry is known without verification too"
    );

    let refused = Client::new(
        &Veto::default().any_port(),
        Options {
            ca_file: Some(own.ca_file.clone()),
            ..Options::default()
        },
    );
    assert!(
        matches!(refused, Err(BuildError::Vetoed(_))),
        "outside module_file_prefixes"
    );
    std::fs::write(own.ca.path().join("empty.pem"), "no pem").unwrap();
    let empty = Client::new(
        &veto,
        Options {
            ca_file: Some(own.ca.path().join("empty.pem")),
            ..Options::default()
        },
    );
    assert!(matches!(empty, Err(BuildError::Invalid(_))));
}

// Redirects of HTTPS: never to HTTP; the certificate is the configured host's.
#[tokio::test]
async fn redirects_of_https() {
    let own = tls(&["localhost"]);
    let veto = Veto {
        file_prefixes: vec![std::fs::canonicalize(own.ca.path()).unwrap()],
        ..Veto::default().any_port()
    };
    let with_ca = client(
        &veto,
        Options {
            ca_file: Some(own.ca_file.clone()),
            ..Options::default()
        },
    );
    let unverified = client(
        &Veto::default().any_port(),
        Options {
            tls_verify: false,
            ..Options::default()
        },
    );
    let downgrade = serve_at(
        "127.0.0.1:0",
        fixed("302 Found", "Location: http://localhost/\r\n", ""),
        Some(own.acceptor.clone()),
    )
    .await;
    let e = with_ca
        .get(
            &url(&format!("https://localhost:{}/", downgrade.addr.port())),
            true,
        )
        .await
        .unwrap_err();
    assert!(e.to_string().contains("from https to http"), "{e}");

    let later = tls_until(&["localhost"], 2045);
    let second = serve_at("127.0.0.1:0", fixed("200 OK", "", ""), Some(later.acceptor)).await;
    let to = format!("https://localhost:{}/", second.addr.port());
    let first = serve_at(
        "127.0.0.1:0",
        Arc::new(move |_, _| answer("302 Found", &format!("Location: {to}\r\n"), "")),
        Some(own.acceptor.clone()),
    )
    .await;
    let r = unverified
        .get(
            &url(&format!("https://localhost:{}/", first.addr.port())),
            true,
        )
        .await
        .unwrap();
    assert_eq!(r.redirects, 1);
    let validity = crate::x509::validity(r.certificate.as_ref().unwrap()).unwrap();
    assert_eq!(validity.not_after, 2_208_988_800, "2040, not 2045");
}

// The Docker API (M5-17) over a socket below module_sockets.
#[tokio::test]
async fn speaks_over_unix_sockets() {
    let dir = crate::TestDir::new();
    let path = dir.path().join("api.sock");
    let listener = UnixListener::bind(&path).unwrap();
    let requests = Arc::new(Mutex::new(Vec::new()));
    let seen = requests.clone();
    tokio::spawn(async move {
        while let Ok((mut stream, _)) = listener.accept().await {
            let request = read_request(&mut stream).await;
            seen.lock().unwrap().push(request);
            let _ = stream.write_all(&answer("200 OK", "", "[]")).await;
        }
    });
    let veto = Veto {
        sockets: vec![std::fs::canonicalize(dir.path()).unwrap()],
        ..Veto::default().any_port()
    };
    let c = client(&veto, Options::default());
    let run = Run::default();
    crate::forget_socket_checks(&path);
    let r = c
        .get_unix(&run, &path, "/v1.43/containers/json", true)
        .await
        .unwrap();
    assert_eq!((r.status, &r.body[..]), (200, &b"[]"[..]));
    assert!(
        crate::checked_off_executor(&path),
        "V1-13: off the executor"
    );
    let request = requests.lock().unwrap()[0].clone();
    assert!(
        request.starts_with("GET /v1.43/containers/json HTTP/1.1\r\n"),
        "{request}"
    );
    assert!(c.get_unix(&run, &path, "no-slash", true).await.is_err());
    let outside = client(&Veto::default().any_port(), Options::default());
    assert!(matches!(
        outside.get_unix(&run, &path, "/", true).await,
        Err(HttpError::Vetoed(_))
    ));
}
