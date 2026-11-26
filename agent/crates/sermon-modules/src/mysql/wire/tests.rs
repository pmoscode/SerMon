use super::*;

fn hex(b: &[u8]) -> String {
    use std::fmt::Write;
    b.iter().fold(String::new(), |mut s, b| {
        let _ = write!(s, "{b:02x}");
        s
    })
}

#[test]
fn scrambles_like_the_servers() {
    let scramble: Vec<u8> = (1..=20).collect();
    assert_eq!(
        hex(&native_password("pencil", &scramble)),
        "cf61fc4b84338d2884baac2131c29caedc133246"
    );
    assert_eq!(
        hex(&sha2_password("pencil", &scramble)),
        "804b188f4565dae5617113370e156183c91cfef6dd4714585f8d465f2150b271"
    );
    assert!(native_password("", &scramble).is_empty() && sha2_password("", &scramble).is_empty());
}

fn fixture(version: &str) -> Vec<u8> {
    let path = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../fixtures/modules/mysql")
        .join(version)
        .join("handshake.bin");
    std::fs::read(&path).unwrap_or_else(|e| panic!("{}: {e}", path.display()))
}

// The greetings of every supported version, as recorded.
#[test]
fn reads_the_handshake_of_every_version() {
    for (version, plugin) in [
        ("mysql-8.4", "caching_sha2_password"),
        ("mysql-9", "caching_sha2_password"),
        ("mariadb-10.11", "mysql_native_password"),
        ("mariadb-11.4", "mysql_native_password"),
        ("mariadb-11.8", "mysql_native_password"),
    ] {
        let h = Handshake::parse(&fixture(version)).unwrap();
        assert_eq!(h.plugin, plugin, "{version}");
        assert_eq!(h.scramble.len(), 20, "{version}");
        assert!(!h.scramble.contains(&0), "{version}");
        assert!(h.capabilities & PLUGIN_AUTH != 0, "{version}");
        // MariaDB's image has TLS from 11.4 on (its own certificate).
        assert_eq!(
            h.capabilities & SSL != 0,
            version != "mariadb-10.11",
            "{version}"
        );
        // MariaDB 10 says 5.5.5-10.11.… for old clients.
        assert!(
            h.version
                .contains(&version[version.find('-').unwrap() + 1..]),
            "{version}: {}",
            h.version
        );
    }
    // A scramble of another length than 20: no server of protocol 4.1.
    let mut long = fixture("mysql-8.4");
    let at = long
        .windows(21)
        .position(|w| w == b"caching_sha2_password")
        .unwrap();
    long.splice(at - 1..at - 1, [b'x'; 10]);
    assert!(Handshake::parse(&long).is_err());
    let refused = [&[0xffu8, 0x69, 0x04][..], b"#HY000Host is blocked"].concat();
    assert!(matches!(
        Handshake::parse(&refused),
        Err(Error::Server { code: 1129, .. })
    ));
    for broken in [
        &[][..],
        &[9][..],
        &[10, b'8', 0][..],
        &[10, 0, 1, 2, 3, 4, 1, 2, 3, 4, 5, 6, 7, 8, 0, 0, 0][..],
    ] {
        assert!(Handshake::parse(broken).is_err(), "{broken:?}");
    }
}

fn lenenc_str(s: &str) -> Vec<u8> {
    [&[u8::try_from(s.len()).unwrap()][..], s.as_bytes()].concat()
}

fn column(name: &str) -> Vec<u8> {
    let mut p = Vec::new();
    for part in ["def", "", "", "", name, name] {
        p.extend(lenenc_str(part));
    }
    p.extend_from_slice(&[0x0c, 0x21, 0, 0, 0, 0, 0xfd, 0, 0, 0, 0, 0]);
    p
}

const EOF: [u8; 5] = [0xfe, 0, 0, 0x02, 0];

#[test]
fn a_query_gives_its_rows() {
    let mut q = Query::default();
    let mut answer = vec![
        vec![2u8],
        column("Variable_name"),
        column("Value"),
        EOF.to_vec(),
    ];
    answer.push([lenenc_str("Queries"), lenenc_str("356")].concat());
    answer.push([lenenc_str("Uptime"), vec![0xfb]].concat());
    answer.push(EOF.to_vec());
    let mut done = None;
    for p in &answer {
        done = q.feed(p).unwrap();
    }
    let set = done.unwrap();
    assert_eq!(set.columns, ["Variable_name", "Value"]);
    assert_eq!(set.rows[1], [Some("Uptime".to_owned()), None]);
    // An OK packet: no result set; an ERR packet: the server's error.
    assert_eq!(
        Query::default().feed(&[0, 0, 0, 2, 0, 0, 0]).unwrap(),
        Some(ResultSet::default())
    );
    let denied = [&[0xffu8, 0xcb, 0x04][..], b"#42000Access denied; you need (at least one of) the SUPER, REPLICATION CLIENT privilege(s)"].concat();
    let Err(Error::Server { code, message }) = Query::default().feed(&denied) else {
        panic!("no error");
    };
    assert_eq!(code, 1227);
    assert!(message.starts_with("Access denied"), "{message}");
}

// The server is foreign: broken answers are errors, never panics, the bounds hold.
#[test]
fn broken_and_large_answers_are_refused() {
    for first in [&[0u8; 0][..], &[0xfb][..], &[200][..], &[0xfc, 0xff][..]] {
        assert!(Query::default().feed(first).is_err(), "{first:?}");
    }
    let mut q = Query::default();
    q.feed(&[1]).unwrap();
    q.feed(&column("a")).unwrap();
    assert!(
        q.feed(&[lenenc_str("x"), lenenc_str("y")].concat())
            .is_err(),
        "no EOF after the columns"
    );
    let mut q = Query::default();
    q.feed(&[1]).unwrap();
    q.feed(&column("a")).unwrap();
    q.feed(&EOF).unwrap();
    assert!(
        q.feed(&[lenenc_str("x"), lenenc_str("y")].concat())
            .is_err(),
        "more values than columns"
    );
    assert!(
        Query::default()
            .feed(&[1])
            .and_then(|_| Query::default().feed(&[0xfe, 0xff, 0xff, 0xff, 0x7f, 0, 0, 0, 0]))
            .is_err()
    );
    let mut q = Query::default();
    q.feed(&[1]).unwrap();
    q.feed(&column("a")).unwrap();
    q.feed(&EOF).unwrap();
    let mut rows = 0;
    let err = loop {
        if let Err(e) = q.feed(&lenenc_str("1")) {
            break e;
        }
        rows += 1;
    };
    assert_eq!(rows, MAX_ROWS);
    assert!(err.to_string().contains("rows"));
    let mut q = Query::default();
    q.feed(&[1]).unwrap();
    q.feed(&column("a")).unwrap();
    q.feed(&EOF).unwrap();
    let big = [vec![0xfc, 0xff, 0xff], vec![b'x'; 0xffff]].concat();
    assert!(
        (0..20)
            .find_map(|_| q.feed(&big).err())
            .is_some_and(|e| e.to_string().contains("bytes"))
    );
    let (packets, rest) = split(&[1, 0, 0, 0, 5, 9, 9]).unwrap();
    assert_eq!((packets, rest.len()), (vec![vec![5]], 2));
    assert!(split(&[0xff, 0xff, 0xff, 0]).is_err(), "multi-packet");
}

/// A server on the other end of a pipe answering the client's packets with `script`.
fn server(
    script: Vec<Vec<u8>>,
) -> (
    tokio::io::DuplexStream,
    tokio::task::JoinHandle<Vec<Vec<u8>>>,
) {
    let (client, io) = tokio::io::duplex(1 << 16);
    let task = tokio::spawn(async move {
        let mut p = Packets::new(io);
        let mut got = Vec::new();
        for answer in script {
            if let Ok(Ok(packet)) =
                tokio::time::timeout(std::time::Duration::from_millis(300), p.read()).await
            {
                got.push(packet);
            }
            if p.write(&answer).await.is_err() {
                break;
            }
        }
        got
    });
    (client, task)
}

/// RFC 5802's example password, nobody's.
const PENCIL: &str = "pencil";

const LOGIN: Login<'static> = Login {
    user: "sermon",
    password: PENCIL,
    database: "",
    secure: false,
};

const OK: [u8; 7] = [0, 0, 0, 2, 0, 0, 0];

#[tokio::test]
async fn logs_in_with_every_path() {
    let scramble: Vec<u8> = (1..=20).collect();
    // An auth switch to mysql_native_password with a new scramble.
    let switch = [&[0xfeu8][..], b"mysql_native_password\0", &scramble, &[0]].concat();
    let (io, sent) = server(vec![switch, OK.to_vec()]);
    let mut p = Packets::new(io);
    p.seq = 2;
    finish_login(&mut p, &LOGIN).await.unwrap();
    assert_eq!(sent.await.unwrap()[0], native_password("pencil", &scramble));

    // caching_sha2_password, fast: 0x01 0x03, then OK.
    let (io, _) = server(vec![vec![1, 3], OK.to_vec()]);
    finish_login(&mut Packets::new(io), &LOGIN).await.unwrap();
    // Full: the password itself, only over TLS or the socket.
    let (io, sent) = server(vec![vec![1, 4], OK.to_vec()]);
    let secure = Login {
        secure: true,
        ..LOGIN
    };
    let mut p = Packets::new(io);
    finish_login(&mut p, &secure).await.unwrap();
    drop(p);
    assert_eq!(sent.await.unwrap()[0], b"pencil\0");
    let (io, sent) = server(vec![vec![1, 4], OK.to_vec()]);
    let Err(Error::Protocol(why)) = finish_login(&mut Packets::new(io), &LOGIN).await else {
        panic!("a full login without TLS");
    };
    assert!(why.contains("TLS"), "{why}");
    assert!(sent.await.unwrap().is_empty(), "the password stayed");

    let denied = [
        &[0xffu8, 0x15, 0x04][..],
        b"#28000Access denied for user 'sermon'@'192.0.2.1' (using password: YES)",
    ]
    .concat();
    let (io, _) = server(vec![denied]);
    assert!(matches!(
        finish_login(&mut Packets::new(io), &LOGIN).await,
        Err(Error::Server { code: 1045, .. })
    ));
    let other = [&[0xfeu8][..], b"client_ed25519\0", &scramble].concat();
    let (io, _) = server(vec![other]);
    assert!(
        matches!(finish_login(&mut Packets::new(io), &LOGIN).await, Err(Error::Protocol(why)) if why.contains("client_ed25519"))
    );
    let (io, _) = server(vec![vec![1, 3]; 8]);
    assert!(
        matches!(finish_login(&mut Packets::new(io), &LOGIN).await, Err(Error::Protocol(why)) if why.contains("doesn't end")),
        "a login without end"
    );
}

#[test]
fn the_handshake_response_names_the_user_and_its_scramble() {
    let h = Handshake::parse(&fixture("mysql-8.4")).unwrap();
    let r = handshake_response(&h, &LOGIN, false).unwrap();
    let caps = u32::from_le_bytes([r[0], r[1], r[2], r[3]]);
    assert_eq!(caps & SSL, 0);
    assert!(r.windows(7).any(|w| w == b"sermon\0"));
    assert!(
        r.windows(32)
            .any(|w| w == sha2_password("pencil", &h.scramble).as_slice())
    );
    assert!(r.ends_with(b"caching_sha2_password\0"));
    assert!(!r.windows(6).any(|w| w == b"pencil"));
    let tls = handshake_response(&h, &LOGIN, true).unwrap();
    assert_ne!(
        u32::from_le_bytes([tls[0], tls[1], tls[2], tls[3]]) & SSL,
        0
    );
    assert_ne!(
        u32::from_le_bytes(ssl_request(h.capabilities)[..4].try_into().unwrap()) & SSL,
        0
    );
    assert!(
        handshake_response(
            &h,
            &Login {
                user: "a\0b",
                ..LOGIN
            },
            false
        )
        .is_err()
    );
}
