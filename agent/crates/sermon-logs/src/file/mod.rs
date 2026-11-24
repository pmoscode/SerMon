//! Classic log files for hosts without journald, like Alpine (M2-20, ADR 0025), and log files
//! the server's configuration names by glob (M3-05).

pub mod glob;
mod multiline;
mod syslog;
mod tail;

pub use multiline::{Assembler, Entry, MAX_ENTRY_BYTES, MAX_ENTRY_LINES, indented};
pub use syslog::{Format, LocalTime, Record, Sender, SyslogError, parse};
pub use tail::{
    Guard, Line, MAX_LINE_BYTES, MAX_READ_BYTES, MAX_READ_LINES, Position, Tail, TailStats,
};
