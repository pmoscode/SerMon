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
    let session = start(io, STARTUP).await.unwrap();
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
        ..STARTUP
    };
    assert!(
        matches!(start(io, without).await, Err(Error::Protocol(why)) if why.contains("password"))
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
