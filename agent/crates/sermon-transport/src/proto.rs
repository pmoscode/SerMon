//! Protocol types generated from `proto/` by `just gen` – never edit the files in `gen/`.

/// `sermon.agent.v1`: the agent ↔ server protocol.
pub mod agent {
    /// Version 1 of the agent protocol.
    #[allow(
        clippy::all,
        clippy::pedantic,
        missing_debug_implementations,
        unused_qualifications,
        reason = "generated code"
    )]
    pub mod v1 {
        // Also pulls in the tonic client (sermon.agent.v1.tonic.rs).
        include!("gen/sermon/agent/v1/sermon.agent.v1.rs");
    }
}
