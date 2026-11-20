//! The systemd journal through `journalctl` as a child process (ADR 0013).

mod cursor;
mod entry;
mod source;

pub use cursor::{Checkpoint, CursorFile};
pub use entry::{
    Boot, Entry, LineError, MAX_CURSOR_BYTES, MAX_FIELD_BYTES, MAX_MESSAGE_BYTES, OUTPUT_FIELDS,
    Seqnum, parse_line, valid_cursor,
};
pub use source::{JournalConfig, JournalSource, JournalStats, arguments};
