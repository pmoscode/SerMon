use super::*;

#[test]
fn md5_follows_rfc_1321() {
    assert_eq!(hex(&md5(b"")), "d41d8cd98f00b204e9800998ecf8427e");
    assert_eq!(hex(&md5(b"abc")), "900150983cd24fb0d6963f7d28e17f72");
    assert_eq!(hex(&md5(&[b'a'; 1000])), "cabe45dcc9ae5b66ba86600cca6b8ba8");
    assert_eq!(
        md5_password("alice", "pencil", &[1, 2, 3, 4]),
        "md537cba386e8b90f1e3941a0e792722253"
    );
}

// RFC 7677's test vector, both ways: the proof we send and the server's signature we check.
#[test]
fn scram_follows_rfc_7677() {
    let mut s = Scram::for_user("user", "pencil", "rOprNGfwEbeRWgbNEkqO");
    assert_eq!(s.client_first(), "n,,n=user,r=rOprNGfwEbeRWgbNEkqO");
    let server =
        "r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096";
    assert_eq!(
        s.client_final(server).unwrap(),
        "c=biws,r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0,p=dHzbZapWIk4jUhN+Ute9ytag9zjfMHgsqmmiz7AndVQ="
    );
    assert!(
        s.verify("v=6rriTRBi23WpRR/wtup+mMhUZUn/dB5nLTJRsjl95G4=")
            .is_ok()
    );
    assert!(
        s.verify("v=AAAATRBi23WpRR/wtup+mMhUZUn/dB5nLTJRsjl95G4=")
            .is_err(),
        "a server that doesn't know the password"
    );
    assert!(s.verify("e=invalid-proof").is_err());
    for bad in [
        "r=other,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096",
        "r=rOprNGfwEbeRWgbNEkqO,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096",
        "r=rOprNGfwEbeRWgbNEkqOx,s=***,i=4096",
        "r=rOprNGfwEbeRWgbNEkqOx,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=100000000",
        "r=rOprNGfwEbeRWgbNEkqOx,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=0",
        "s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096",
    ] {
        let mut s = Scram::for_user("user", "pencil", "rOprNGfwEbeRWgbNEkqO");
        assert!(s.client_final(bad).is_err(), "{bad}");
    }
}

fn row_desc(columns: &[&str]) -> Message {
    let mut body = i16::try_from(columns.len()).unwrap().to_be_bytes().to_vec();
    for c in columns {
        body.extend_from_slice(c.as_bytes());
        body.push(0);
        body.extend_from_slice(&[0; 18]);
    }
    Message { tag: b'T', body }
}

fn row(values: &[Option<&str>]) -> Message {
    let mut body = i16::try_from(values.len()).unwrap().to_be_bytes().to_vec();
    for v in values {
        match v {
            None => body.extend_from_slice(&(-1i32).to_be_bytes()),
            Some(v) => {
                body.extend_from_slice(&i32::try_from(v.len()).unwrap().to_be_bytes());
                body.extend_from_slice(v.as_bytes());
            }
        }
    }
    Message { tag: b'D', body }
}

fn tag(t: u8, body: &[u8]) -> Message {
    Message {
        tag: t,
        body: body.to_vec(),
    }
}

#[test]
fn a_query_gives_its_result_sets() {
    let mut q = Query::default();
    let messages = [
        row_desc(&["state", "count"]),
        row(&[Some("active"), Some("3")]),
        row(&[None, Some("1")]),
        tag(b'C', b"SELECT 2\0"),
        tag(b'N', b"SNOTICE\0Mhello\0\0"),
        row_desc(&["x"]),
        tag(b'C', b"SELECT 0\0"),
        tag(b'I', b""),
    ];
    for m in &messages {
        assert!(q.feed(m).unwrap().is_none());
    }
    let sets = q.feed(&tag(b'Z', b"I")).unwrap().unwrap().unwrap();
    assert_eq!(sets.len(), 3);
    assert_eq!(sets[0].columns, ["state", "count"]);
    assert_eq!(sets[0].rows[1], [None, Some("1".to_owned())]);
    assert_eq!(sets[1].rows, Vec::<Vec<Option<String>>>::new());

    // An error: the server's code and message once it is ready again.
    let mut q = Query::default();
    q.feed(&tag(
        b'E',
        b"SERROR\0C42501\0Mpermission denied for function pg_ls_dir\0\0",
    ))
    .unwrap();
    let Some(Err(Error::Server { code, message })) = q.feed(&tag(b'Z', b"I")).unwrap() else {
        panic!("no error");
    };
    assert_eq!(
        (code.as_str(), message.as_str()),
        ("42501", "permission denied for function pg_ls_dir")
    );
}

// The server's messages are foreign: broken ones are errors, never panics, and the bounds hold.
#[test]
fn broken_and_large_answers_are_refused() {
    let mut q = Query::default();
    assert!(
        q.feed(&row(&[Some("x")])).is_err(),
        "a row without description"
    );
    for broken in [
        tag(b'T', &[0, 1, b'x']),
        tag(b'T', &[0xff, 0xff]),
        tag(b'T', &[0, 200]),
        tag(b'G', b""),
    ] {
        assert!(Query::default().feed(&broken).is_err(), "{broken:?}");
    }
    let mut q = Query::default();
    q.feed(&row_desc(&["a", "b"])).unwrap();
    assert!(q.feed(&row(&[Some("1")])).is_err(), "too few values");
    assert!(
        q.feed(&tag(b'D', &[0, 1, 0x7f, 0xff, 0xff, 0xff])).is_err(),
        "a value beyond the message"
    );

    let mut q = Query::default();
    q.feed(&row_desc(&["a"])).unwrap();
    let mut rows = 0;
    let err = loop {
        if let Err(e) = q.feed(&row(&[Some("1")])) {
            break e;
        }
        rows += 1;
    };
    assert_eq!(rows, MAX_ROWS);
    assert!(err.to_string().contains("rows"));
    let mut q = Query::default();
    q.feed(&row_desc(&["a"])).unwrap();
    let big = "x".repeat(MAX_ANSWER / 4);
    let err = (0..8)
        .find_map(|_| q.feed(&row(&[Some(&big)])).err())
        .unwrap();
    assert!(err.to_string().contains("bytes"));

    let (messages, rest) = split(&[b'Z', 0, 0, 0, 5, b'I', b'C', 0]).unwrap();
    assert_eq!((messages.len(), rest.len()), (1, 2));
    assert!(split(&[b'Z', 0, 0, 0, 3]).is_err());
    assert!(split(&[b'Z', 0x7f, 0, 0, 0]).is_err());
    assert_eq!(
        error_fields(b"C123456789\0M"),
        ("12345".to_owned(), String::new())
    );
}

/// A server on the other end of a pipe: answers with `script` after reading the startup.
fn server(script: Vec<u8>) -> (tokio::io::DuplexStream, tokio::task::JoinHandle<Vec<u8>>) {
    let (client, mut server) = tokio::io::duplex(1 << 16);
    let task = tokio::spawn(async move {
        let mut got = vec![0u8; 4096];
        let n = server.read(&mut got).await.unwrap();
        server.write_all(&script).await.unwrap();
        let mut more = vec![0u8; 4096];
        let m = tokio::time::timeout(
            std::time::Duration::from_millis(200),
            server.read(&mut more),
        )
        .await
        .map_or(0, |r| r.unwrap_or(0));
        got.truncate(n);
        got.extend_from_slice(&more[..m]);
        got
    });
    (client, task)
}

fn frame(m: &Message) -> Vec<u8> {
    message(m.tag, &m.body)
}

const STARTUP: Startup<'static> = Startup {
    user: "sermon",
    database: "postgres",
    password: Some("pencil"),
    options: "-c statement_timeout=3000",
    allow: Allow::Scram,
    secure: false,
};

const MD5: Startup<'static> = Startup {
    allow: Allow::Md5,
    ..STARTUP
};

#[tokio::test]
async fn starts_with_md5_and_reports_refusals() {
    let script = [
        frame(&tag(b'R', &[0, 0, 0, 5, 1, 2, 3, 4])),
        frame(&tag(b'R', &[0, 0, 0, 0])),
        frame(&tag(b'S', b"server_version\x0017.2\0")),
        frame(&tag(b'K', &[0; 8])),
        frame(&tag(b'Z', b"I")),
    ]
    .concat();
    let (io, sent) = server(script);
    let session = start(io, MD5).await.unwrap();
    drop(session);
    let sent = sent.await.unwrap();
    let text = String::from_utf8_lossy(&sent);
    assert!(text.contains("user\0sermon\0database\0postgres\0application_name\0sermon-agent\0options\0-c statement_timeout=3000\0"), "{text}");
    assert!(
        text.contains(&md5_password("sermon", "pencil", &[1, 2, 3, 4])),
        "{text}"
    );
    assert!(!text.contains("pencil"), "the password itself");

    let (io, _) = server(frame(&tag(
        b'E',
        b"SFATAL\0C28P01\0Mpassword authentication failed for user \"sermon\"\0\0",
    )));
    let Err(Error::Server { code, .. }) = start(io, STARTUP).await else {
        panic!("no refusal");
    };
    assert_eq!(code, "28P01");

    let (io, _) = server(frame(&tag(b'R', &[0, 0, 0, 7])));
    assert!(
        matches!(start(io, STARTUP).await, Err(Error::Protocol(why)) if why.contains("method 7"))
    );
    let (io, _) = server(frame(&tag(b'R', &[0, 0, 0, 3])));
    let without = Startup {
        password: None,
        allow: Allow::Password,
        secure: true,
        ..STARTUP
    };
    assert!(
        matches!(start(io, without).await, Err(Error::Protocol(why)) if why.contains("set the module's password"))
    );
    let (io, _) = server(frame(&tag(b'R', b"\0\0\0\x0aSCRAM-SHA-256-PLUS\0\0")));
    assert!(
        matches!(start(io, STARTUP).await, Err(Error::Protocol(why)) if why.contains("SCRAM-SHA-256"))
    );
    let (io, _) = server(frame(&tag(b'R', b"\0\0\0\x0cv=xx")));
    assert!(
        start(io, STARTUP).await.is_err(),
        "SCRAM's end without its start"
    );
    let (io, _) = server(vec![b'R', 0x7f, 0, 0, 0]);
    assert!(start(io, STARTUP).await.is_err(), "too long");
    let bad = Startup {
        user: "a\0b",
        ..STARTUP
    };
    let (io, _) = server(Vec::new());
    assert!(start(io, bad).await.is_err());
}

#[tokio::test]
async fn asks_for_tls_reading_one_byte() {
    let (mut client, mut server) = tokio::io::duplex(64);
    server.write_all(b"Sxyz").await.unwrap();
    assert!(ssl_request(&mut client).await.unwrap());
    let mut rest = [0u8; 3];
    client.read_exact(&mut rest).await.unwrap();
    assert_eq!(&rest, b"xyz", "nothing after the answer was taken");
    server.write_all(b"N").await.unwrap();
    let answer = tokio::time::timeout(std::time::Duration::from_secs(2), ssl_request(&mut client));
    assert!(!answer.await.expect("one byte is the answer").unwrap());
    server.write_all(b"E").await.unwrap();
    assert!(ssl_request(&mut client).await.is_err());
}

/// What a scripted server does once the client sent its client-final-message.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Sasl {
    /// Its signature, `AuthenticationOk`, ready.
    Honest,
    /// `AuthenticationOk` and ready without its signature.
    SkipsTheSignature,
    /// A signature of a server that doesn't know the password.
    WrongSignature,
    /// Asks for the password in clear text after all.
    Cleartext,
    /// Asks for md5 after all.
    Md5,
    /// Sends its server-first-message again (another PBKDF2 round).
    AgainContinue,
    /// Starts SCRAM again.
    AgainStart,
}

async fn read_message(io: &mut tokio::io::DuplexStream) -> Option<Message> {
    let mut head = [0u8; 5];
    io.read_exact(&mut head).await.ok()?;
    let len = usize::try_from(u32::from_be_bytes([head[1], head[2], head[3], head[4]])).ok()?;
    let mut body = vec![0; len - 4];
    io.read_exact(&mut body).await.ok()?;
    Some(Message { tag: head[0], body })
}

fn auth(method: i32, data: &[u8]) -> Vec<u8> {
    frame(&tag(b'R', &[&method.to_be_bytes()[..], data].concat()))
}

/// The server's signature of SCRAM-SHA-256 for `password` (RFC 5802).
fn server_signature(password: &str, salt: &[u8], auth_message: &str) -> String {
    let mut salted = [0u8; 32];
    pbkdf2::derive(
        pbkdf2::PBKDF2_HMAC_SHA256,
        NonZeroU32::new(4096).unwrap(),
        salt,
        password.as_bytes(),
        &mut salted,
    );
    let server_key = hmac::sign(&hmac::Key::new(hmac::HMAC_SHA256, &salted), b"Server Key");
    let sig = hmac::sign(
        &hmac::Key::new(hmac::HMAC_SHA256, server_key.as_ref()),
        auth_message.as_bytes(),
    );
    B64.encode(sig.as_ref())
}

/// A server that starts SCRAM-SHA-256 knowing `password` and then does `how`; returns what the
/// client sent after its startup.
fn sasl_server(
    password: &'static str,
    how: Sasl,
) -> (tokio::io::DuplexStream, tokio::task::JoinHandle<Vec<u8>>) {
    let (client, mut io) = tokio::io::duplex(1 << 16);
    let task = tokio::spawn(async move {
        let mut len = [0u8; 4];
        io.read_exact(&mut len).await.unwrap();
        let mut startup = vec![0; usize::try_from(u32::from_be_bytes(len)).unwrap() - 4];
        io.read_exact(&mut startup).await.unwrap();
        let mut sent = Vec::new();
        io.write_all(&auth(10, b"SCRAM-SHA-256\0\0")).await.unwrap();
        let first = read_message(&mut io).await.unwrap();
        sent.extend_from_slice(&first.body);
        let first_bare = String::from_utf8_lossy(&first.body[18..])
            .trim_start_matches("n,,")
            .to_owned();
        let nonce = first_bare.split("r=").nth(1).unwrap().to_owned();
        let salt = b"pepper-salt-0123";
        let server_first = format!("r={nonce}srv,s={},i=4096", B64.encode(salt));
        io.write_all(&auth(11, server_first.as_bytes()))
            .await
            .unwrap();
        let fin = read_message(&mut io).await.unwrap();
        sent.extend_from_slice(&fin.body);
        let fin = String::from_utf8_lossy(&fin.body).into_owned();
        let without_proof = fin.split(",p=").next().unwrap();
        let signature = server_signature(
            password,
            salt,
            &format!("{first_bare},{server_first},{without_proof}"),
        );
        let ready = [
            auth(0, &[]),
            frame(&tag(b'S', b"server_version\x0017.2\0")),
            frame(&tag(b'K', &[0; 8])),
            frame(&tag(b'Z', b"I")),
        ]
        .concat();
        let script = match how {
            Sasl::Honest => [auth(12, format!("v={signature}").as_bytes()), ready].concat(),
            Sasl::SkipsTheSignature => ready,
            Sasl::WrongSignature => [
                auth(12, format!("v={}", B64.encode([7u8; 32])).as_bytes()),
                ready,
            ]
            .concat(),
            Sasl::Cleartext => auth(3, &[]),
            Sasl::Md5 => auth(5, &[1, 2, 3, 4]),
            Sasl::AgainContinue => auth(11, server_first.as_bytes()),
            Sasl::AgainStart => auth(10, b"SCRAM-SHA-256\0\0"),
        };
        let _ = io.write_all(&script).await;
        let mut more = vec![0u8; 4096];
        while let Ok(Ok(n)) =
            tokio::time::timeout(std::time::Duration::from_millis(200), io.read(&mut more)).await
        {
            if n == 0 {
                break;
            }
            sent.extend_from_slice(&more[..n]);
        }
        sent
    });
    (client, task)
}

// M5-33: a server (or a local impostor on the port) that started SCRAM must prove it knows the
// password; it can't skip that, and it can't switch to a method that gets the password or a hash
// of it.
#[tokio::test]
async fn scram_can_be_neither_skipped_nor_downgraded() {
    for allow in [Allow::Scram, Allow::Md5, Allow::Password] {
        let startup = Startup {
            allow,
            secure: true,
            ..STARTUP
        };
        let (io, _) = sasl_server("pencil", Sasl::Honest);
        assert!(start(io, startup).await.is_ok(), "{allow:?}");
        for how in [
            Sasl::SkipsTheSignature,
            Sasl::WrongSignature,
            Sasl::Cleartext,
            Sasl::Md5,
            Sasl::AgainContinue,
            Sasl::AgainStart,
        ] {
            let (io, sent) = sasl_server("pencil", how);
            let result = start(io, startup).await;
            assert!(
                matches!(result, Err(Error::Protocol(_))),
                "{allow:?} {how:?}: {result:?}"
            );
            let sent = sent.await.unwrap();
            let text = String::from_utf8_lossy(&sent);
            assert!(!text.contains("pencil"), "{allow:?} {how:?}: the password");
            assert!(
                !text.contains(&md5_password("sermon", "pencil", &[1, 2, 3, 4])),
                "{allow:?} {how:?}: its md5 hash"
            );
        }
    }
    let (io, _) = sasl_server("another", Sasl::Honest);
    assert!(
        matches!(start(io, STARTUP).await, Err(Error::Protocol(why)) if why.contains("signature")),
        "a server with another password"
    );
}

// Without SCRAM: md5 only when allowed, the password itself only when allowed and over the socket
// or verified TLS; a session only after AuthenticationOk.
#[tokio::test]
async fn weaker_methods_only_when_allowed() {
    let ready = [frame(&tag(b'Z', b"I"))].concat();
    let ok = [auth(0, &[]), ready.clone()].concat();
    let cases = [
        (STARTUP, 3, false),
        (MD5, 3, false),
        (
            Startup {
                allow: Allow::Password,
                ..STARTUP
            },
            3,
            false,
        ),
        (
            Startup {
                secure: true,
                ..MD5
            },
            3,
            false,
        ),
        (
            Startup {
                allow: Allow::Password,
                secure: true,
                ..STARTUP
            },
            3,
            true,
        ),
        (
            Startup {
                secure: true,
                ..STARTUP
            },
            5,
            false,
        ),
        (MD5, 5, true),
        (
            Startup {
                allow: Allow::Password,
                ..STARTUP
            },
            5,
            true,
        ),
    ];
    for (startup, method, taken) in cases {
        let data: &[u8] = if method == 5 { &[1, 2, 3, 4] } else { &[] };
        let (io, sent) = server([auth(method, data), ok.clone()].concat());
        let result = start(io, startup).await;
        assert_eq!(result.is_ok(), taken, "{startup:?} {method}: {result:?}");
        let sent = sent.await.unwrap();
        let text = String::from_utf8_lossy(&sent);
        assert_eq!(
            text.contains("pencil\0"),
            taken && method == 3,
            "{startup:?} {method}"
        );
        assert_eq!(
            text.contains(&md5_password("sermon", "pencil", &[1, 2, 3, 4])),
            taken && method == 5,
            "{startup:?} {method}"
        );
    }
    // trust: no password asked, none sent.
    let (io, sent) = server(ok.clone());
    assert!(start(io, STARTUP).await.is_ok());
    assert!(!String::from_utf8_lossy(&sent.await.unwrap()).contains("pencil"));
    // Ready, parameters or a second AuthenticationOk out of place.
    for script in [
        ready.clone(),
        [frame(&tag(b'S', b"a\0b\0")), ok.clone()].concat(),
        [frame(&tag(b'K', &[0; 8])), ok.clone()].concat(),
        [auth(0, &[]), auth(0, &[]), ready.clone()].concat(),
        [auth(5, &[1, 2, 3, 4]), auth(5, &[1, 2, 3, 4]), ok.clone()].concat(),
    ] {
        let (io, _) = server(script);
        assert!(start(io, MD5).await.is_err());
    }
}

// The state machine on its own: authenticated only after AuthenticationOk in its place.
#[test]
fn the_login_is_authenticated_only_at_its_end() {
    let mut login = Login::with_nonce(STARTUP, "fixed-nonce");
    assert!(!login.authenticated());
    let Step::Send(first) = login
        .feed(&tag(b'R', b"\0\0\0\x0aSCRAM-SHA-256\0\0"))
        .unwrap()
    else {
        panic!("no client-first-message");
    };
    assert!(String::from_utf8_lossy(&first).contains("n,,n=,r=fixed-nonce"));
    assert!(login.feed(&tag(b'R', &[0, 0, 0, 0])).is_err());
    assert!(!login.authenticated());
    let started = login.startup_message().unwrap();
    assert_eq!(
        usize::try_from(u32::from_be_bytes([
            started[0], started[1], started[2], started[3]
        ]))
        .unwrap(),
        started.len()
    );
}
