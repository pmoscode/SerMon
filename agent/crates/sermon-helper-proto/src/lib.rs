//! `sermon.helper.v1`: the protocol between the agent and the root helper on one host (ADR 0035),
//! generated from `proto/sermon/helper` by `just gen` – never edit the files in `gen/`.

/// Version 1 of the helper protocol.
#[allow(
    clippy::all,
    clippy::pedantic,
    missing_debug_implementations,
    unused_qualifications,
    reason = "generated code"
)]
pub mod v1 {
    include!("gen/sermon/helper/v1/sermon.helper.v1.rs");
}
