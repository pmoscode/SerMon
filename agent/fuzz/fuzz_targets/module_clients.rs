//! What the modules' clients take from foreign services besides their parsers (M5-33): the
//! reply reader of Redis, the checks of certificates (PEM from a file or an endpoint) and the
//! positions and name patterns of the containers' output. The first byte picks; no input may
//! panic, and what is taken stays in its bounds.
#![no_main]

use std::future::Future;
use std::pin::pin;
use std::task::{Context, Poll, Waker};

use libfuzzer_sys::fuzz_target;
use sermon_modules::docker_socket::logs;
use sermon_modules::{redis, tls};

/// Polls `f` until it is done; a reader over a slice never waits.
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

fuzz_target!(|data: &[u8]| {
    let Some((&pick, rest)) = data.split_first() else {
        return;
    };
    match pick % 4 {
        0 => {
            let mut reader = tokio::io::BufReader::new(rest);
            match ready(redis::reply(&mut reader)) {
                Some(Ok(redis::Reply::Bulk(body))) => assert!(body.len() <= redis::MAX_INFO),
                Some(Ok(redis::Reply::Simple(t) | redis::Reply::Error(t))) => {
                    assert!(t.len() <= 3 * 1024);
                }
                Some(Ok(redis::Reply::Nil) | Err(_)) => {}
                None => panic!("a reply over a slice waits"),
            }
        }
        1 => {
            let (now, text) = rest.split_at(rest.len().min(4));
            let now = i64::from(u32::from_be_bytes([
                *now.first().unwrap_or(&0),
                *now.get(1).unwrap_or(&0),
                *now.get(2).unwrap_or(&0),
                *now.get(3).unwrap_or(&0),
            ]));
            if let Some((leaf, chain, _, _)) = tls::judge_pem(text, now, "example.org") {
                assert!(chain <= leaf, "the chain ends with its leaf at the latest");
            }
        }
        2 => {
            let tty = pick & 0x80 != 0;
            let lines = logs::parse(rest, tty);
            // The two streams of the multiplexed answer merged by time; a TTY's stays as written.
            assert!(
                tty || lines
                    .windows(2)
                    .all(|w| (w[0].secs, w[0].nanos) <= (w[1].secs, w[1].nanos))
            );
            let mut position = logs::Position::at(0, 0);
            let first = position.take(lines.clone(), logs::PER_CONTAINER);
            assert!(first.len() <= logs::PER_CONTAINER);
            let again = position.take(lines.clone(), logs::PER_CONTAINER);
            assert!(first.len() + again.len() <= lines.len());
            for l in first.iter().chain(&again) {
                assert!(l.secs >= 0 && !l.message.contains('\n'));
            }
        }
        _ => {
            let text = String::from_utf8_lossy(rest);
            let (pattern, name) = text.split_once('\0').unwrap_or((&text, ""));
            let hit = logs::matches(pattern, name);
            if !pattern.contains('*') {
                assert_eq!(hit, pattern == name);
            }
            assert!(logs::matches("*", name));
        }
    }
});
