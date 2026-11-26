//! The parts of the root helper (ADR 0035) apart from its service loop, also for the fuzz target
//! `helper_request`.
// Only through `log`: eprintln! panics when nobody reads the log any more (M4-31).
#![deny(clippy::print_stderr)]

/// The helper's version: `SERMON_VERSION` at build time (the release, as in its signature), else
/// the crate's (M4-31: a self-update replaces only an older helper).
pub const VERSION: &str = match option_env!("SERMON_VERSION") {
    Some(v) => v,
    None => env!("CARGO_PKG_VERSION"),
};

/// Writes a line to the log (stderr); a log nobody reads any more – the pipe of an OpenRC service
/// that restarted – is no reason to end a run (M4-31).
pub fn log(line: &str) {
    use std::io::Write;
    let _ = writeln!(std::io::stderr().lock(), "{line}");
}

pub mod command;
pub mod config;
pub mod host;
pub mod minisign;
pub mod peer;
pub mod request;
pub mod wire;
