//! `gRPC` transport to the `SerMon` server: enrollment, `mTLS` stream, certificate rotation.

pub mod channel;
mod convert;
pub mod enrollment;
pub mod fingerprint;
pub mod identity;
pub mod proto;
pub mod tls;
