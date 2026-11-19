//! Write-ahead log buffering agent data on disk until the server acknowledges it (KONZEPT §4.1, §5).
//!
//! - Every appended payload gets a [`Seq`]; the server acknowledges up to a `Seq` (inclusive) and
//!   the buffer forgets everything up to there (*at-least-once*, the server deduplicates).
//! - Data lives in segment files; whole segments are deleted after the ack or when a limit
//!   (bytes, age) is reached – oldest first, counted as dropped.
//! - After a crash the log is cut at the first damaged record; everything before it survives.

mod format;
mod wal;

use std::fmt;
use std::path::PathBuf;
use std::time::Duration;

pub use format::{Corruption, Record};
pub use wal::{Recovery, Wal, WalStats};

/// Sequence number of a buffered record. Increases by one per record and never repeats, also
/// not across restarts or after the buffer directory was lost (see [`Wal::open`]).
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash, Default)]
pub struct Seq(u64);

impl Seq {
    /// Wraps a raw sequence number (e.g. from an `Ack`).
    #[must_use]
    pub const fn new(value: u64) -> Self {
        Self(value)
    }

    /// The raw number (for the protocol).
    #[must_use]
    pub const fn get(self) -> u64 {
        self.0
    }

    /// The following number; saturates at the maximum (never reached in practice).
    #[must_use]
    pub const fn next(self) -> Self {
        Self(self.0.saturating_add(1))
    }
}

impl fmt::Display for Seq {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        self.0.fmt(f)
    }
}

/// When appended data is forced to disk (`fsync`).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum FsyncPolicy {
    /// After every append: nothing is lost on power failure, at the cost of one fsync per record.
    Always,
    /// At most this long after an append (checked on the next append and by [`Wal::sync`]).
    Interval(Duration),
    /// Leave it to the operating system; data survives a crash of the agent, but not
    /// necessarily a power failure.
    Never,
}

/// Limits and behaviour of the write-ahead log.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct WalConfig {
    /// Upper bound for all segment files together (KONZEPT §4.1: 200 MB).
    pub max_bytes: u64,
    /// Records older than this are dropped (KONZEPT §4.1: 24 h).
    pub max_age: Duration,
    /// A segment is closed once it reaches this size.
    pub segment_bytes: u64,
    /// A segment is closed once its oldest record is this old; bounds how late the age limit
    /// takes effect.
    pub segment_span: Duration,
    /// Largest accepted payload.
    pub max_record_bytes: u64,
    /// When to fsync.
    pub fsync: FsyncPolicy,
}

impl Default for WalConfig {
    fn default() -> Self {
        Self {
            max_bytes: 200 * 1024 * 1024,
            max_age: Duration::from_hours(24),
            segment_bytes: 4 * 1024 * 1024,
            segment_span: Duration::from_hours(1),
            max_record_bytes: 4 * 1024 * 1024,
            fsync: FsyncPolicy::Interval(Duration::from_secs(5)),
        }
    }
}

impl WalConfig {
    /// Checks that the limits fit together.
    ///
    /// # Errors
    ///
    /// [`WalError::InvalidConfig`] naming the violated rule.
    pub fn validate(&self) -> Result<(), WalError> {
        let invalid = |reason: &str| Err(WalError::InvalidConfig(reason.to_owned()));
        if self.max_record_bytes == 0 || self.segment_bytes == 0 {
            return invalid("segment_bytes and max_record_bytes must be positive");
        }
        // The active segment is never dropped, so it plus one record must fit into the limit.
        let active_worst_case = self.segment_bytes.saturating_add(self.max_record_bytes);
        if active_worst_case.saturating_add(format::RECORD_HEADER_LEN) > self.max_bytes {
            return invalid("max_bytes must hold a full segment plus the largest record");
        }
        if self.segment_span.is_zero() || self.segment_span >= self.max_age {
            return invalid("segment_span must be positive and shorter than max_age");
        }
        Ok(())
    }
}

/// Errors of the write-ahead log.
#[derive(Debug, thiserror::Error)]
pub enum WalError {
    /// File system error.
    #[error("{path}: {source}")]
    Io {
        /// File or directory.
        path: PathBuf,
        /// Cause.
        #[source]
        source: std::io::Error,
    },
    /// Another process uses the directory.
    #[error("{0} is used by another process")]
    Locked(PathBuf),
    /// The payload exceeds [`WalConfig::max_record_bytes`].
    #[error("record of {len} bytes exceeds the limit of {max} bytes")]
    RecordTooLarge {
        /// Size of the payload.
        len: u64,
        /// The limit.
        max: u64,
    },
    /// The server acknowledged a sequence number that was never appended.
    #[error("ack {ack} is beyond the last appended record {last}")]
    AckBeyondEnd {
        /// The acknowledged number.
        ack: Seq,
        /// The last appended number.
        last: Seq,
    },
    /// A segment changed on disk after it was checked.
    #[error("{path} at byte {offset}: {reason}")]
    Corrupt {
        /// Segment file.
        path: PathBuf,
        /// Position of the damaged record.
        offset: u64,
        /// What is wrong.
        reason: Corruption,
    },
    /// The configuration is inconsistent.
    #[error("invalid buffer configuration: {0}")]
    InvalidConfig(String),
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn default_config_is_valid() {
        assert!(WalConfig::default().validate().is_ok());
    }

    #[test]
    fn rejects_inconsistent_limits() {
        let base = WalConfig::default();
        let cases = [
            WalConfig {
                segment_bytes: 0,
                ..base.clone()
            },
            WalConfig {
                max_bytes: base.segment_bytes,
                ..base.clone()
            },
            WalConfig {
                segment_span: base.max_age,
                ..base.clone()
            },
            WalConfig {
                segment_span: Duration::ZERO,
                ..base.clone()
            },
        ];
        for config in cases {
            assert!(
                matches!(config.validate(), Err(WalError::InvalidConfig(_))),
                "{config:?}"
            );
        }
    }

    #[test]
    fn seq_orders_and_advances() {
        assert!(Seq::new(1) < Seq::new(2));
        assert_eq!(Seq::new(1).next(), Seq::new(2));
        assert_eq!(Seq::new(u64::MAX).next(), Seq::new(u64::MAX));
        assert_eq!(Seq::new(42).to_string(), "42");
    }
}
