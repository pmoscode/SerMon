//! The logins of the database clients (M5-33) against a foreign server: what a PostgreSQL or
//! MySQL server (or whoever answers on its port) sends during the authentication. No input may
//! panic; the password goes out in clear text only when allowed and over a secure channel, its md5
//! hash only when allowed; a PostgreSQL session never opens after SCRAM started (a fuzzer can't
//! forge the server's signature) or before `AuthenticationOk`.
#![no_main]

use std::future::Future;
use std::pin::pin;
use std::task::{Context, Poll, Waker};

use libfuzzer_sys::fuzz_target;
use sermon_modules::mysql::wire as my;
use sermon_modules::postgres::wire::{self as pg, Allow, Login, Startup, Step};

/// RFC 5802's example password, nobody's.
const PENCIL: &str = "pencil";

/// Polls `f` until it is done; the streams below are in memory and never wait.
fn ready<F: Future>(f: F) -> Option<F::Output> {
    let mut f = pin!(f);
    let mut cx = Context::from_waker(Waker::noop());
    for _ in 0..10_000 {
        if let Poll::Ready(out) = f.as_mut().poll(&mut cx) {
            return Some(out);
        }
    }
    None
}

fn postgres(switch: u8, data: &[u8]) {
    let allow = match switch % 3 {
        0 => Allow::Scram,
        1 => Allow::Md5,
        _ => Allow::Password,
    };
    let secure = switch & 0x80 != 0;
    let startup = Startup {
        user: "sermon",
        database: "postgres",
        password: Some(PENCIL),
        options: "",
        allow,
        secure,
    };
    let Ok((messages, _)) = pg::split(data) else {
        return;
    };
    let mut login = Login::with_nonce(startup, "rOprNGfwEbeRWgbNEkqO");
    let mut sasl = false;
    for m in &messages {
        match login.feed(m) {
            Ok(Step::Send(reply)) => {
                let body = reply.get(5..).unwrap_or_default();
                sasl |= body.starts_with(b"SCRAM-SHA-256\0");
                if body == b"pencil\0" {
                    assert!(
                        allow == Allow::Password && secure,
                        "the password in clear text"
                    );
                }
                if body.starts_with(b"md5") {
                    assert!(allow != Allow::Scram, "md5 not allowed");
                }
            }
            Ok(Step::Wait) => {}
            Ok(Step::Ready) => {
                assert!(login.authenticated());
                assert!(
                    !sasl,
                    "a session after SCRAM without the server's signature"
                );
                break;
            }
            Err(_) => break,
        }
    }
}

fn mysql(switch: u8, data: &[u8]) {
    let secure = switch & 0x80 != 0;
    let login = my::Login {
        user: "sermon",
        password: PENCIL,
        database: "",
        secure,
    };
    let mut sent = Vec::new();
    {
        let io = tokio::io::join(data, &mut sent);
        let mut packets = my::Packets::new(io);
        let _ = ready(my::finish_login(&mut packets, &login));
    }
    if sent.windows(7).any(|w| w == b"pencil\0") {
        assert!(secure, "the full login over an insecure channel");
    }
}

fuzz_target!(|data: &[u8]| {
    let Some((&switch, rest)) = data.split_first() else {
        return;
    };
    if switch & 0x40 == 0 {
        postgres(switch, rest);
    } else {
        mysql(switch, rest);
    }
});
