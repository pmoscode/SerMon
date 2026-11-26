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

        // A credential of a module (ADR 0039) prints its module and field, never its value:
        // the generated Debug is skipped (buf.gen.yaml).
        impl std::fmt::Debug for ModuleSecret {
            fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
                f.debug_struct("ModuleSecret")
                    .field("module_id", &self.module_id)
                    .field("field", &self.field)
                    .field("value", &"<redacted>")
                    .finish()
            }
        }
    }
}
