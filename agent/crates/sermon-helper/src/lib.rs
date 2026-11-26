//! The parts of the root helper (ADR 0035) apart from its service loop, also for the fuzz target
//! `helper_request`.

/// The helper's version: `SERMON_VERSION` at build time (the release, as in its signature), else
/// the crate's (M4-31: a self-update replaces only an older helper).
pub const VERSION: &str = match option_env!("SERMON_VERSION") {
    Some(v) => v,
    None => env!("CARGO_PKG_VERSION"),
};

pub mod command;
pub mod config;
pub mod host;
pub mod minisign;
pub mod peer;
pub mod request;
pub mod wire;
