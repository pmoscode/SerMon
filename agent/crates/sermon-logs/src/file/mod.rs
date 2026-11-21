//! Classic log files for hosts without journald, like Alpine (M2-20, ADR 0025).

mod syslog;
mod tail;

pub use syslog::{Format, LocalTime, Record, Sender, SyslogError, parse};
pub use tail::{Line, MAX_LINE_BYTES, MAX_READ_BYTES, Position, Tail, TailStats};
