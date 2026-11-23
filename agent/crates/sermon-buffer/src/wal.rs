//! The write-ahead log: segment files in one directory.
//!
//! ```text
//! <dir>/lock                        exclusive lock of the running agent
//! <dir>/acked                       last acknowledged Seq (u64 + CRC-32)
//! <dir>/seg-<first seq, 20 digits>.wal
//! ```
//!
//! The newest segment is the *active* one; only it is appended to and it is never deleted, so its
//! name keeps the sequence counter alive even when everything else was acknowledged.

use std::collections::VecDeque;
use std::fs::{self, DirBuilder, File, OpenOptions, TryLockError};
use std::io::{self, BufReader, Read, Seek, SeekFrom, Write};
use std::os::unix::fs::{DirBuilderExt, FileExt, OpenOptionsExt};
use std::path::{Path, PathBuf};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use crate::format::{self, Corruption, MAGIC, ReadError, Record};
use crate::{FsyncPolicy, Seq, WalConfig, WalError};

const LOCK_FILE: &str = "lock";
const ACK_FILE: &str = "acked";
const SEGMENT_PREFIX: &str = "seg-";
const SEGMENT_SUFFIX: &str = ".wal";
const MAGIC_LEN: u64 = 8;

/// What [`Wal::open`] found and repaired.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Recovery {
    /// Segments in use after the recovery.
    pub segments: usize,
    /// Records in these segments (acknowledged or not).
    pub records: u64,
    /// Segments cut at a damaged record, with the number of bytes removed.
    pub truncated: Vec<(PathBuf, u64)>,
    /// Unreadable segment files that were deleted.
    pub removed: Vec<PathBuf>,
}

/// Counters for monitoring the buffer.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct WalStats {
    /// Sequence number of the next append.
    pub next_seq: Seq,
    /// Highest acknowledged sequence number.
    pub acked: Seq,
    /// Records not yet acknowledged.
    pub pending_records: u64,
    /// Size of all segment files.
    pub bytes: u64,
    /// Number of segment files.
    pub segments: usize,
    /// Unacknowledged records dropped because of the byte or age limit since the start.
    pub dropped_records: u64,
    /// Number of fsync calls since the start.
    pub syncs: u64,
}

#[derive(Debug, Clone)]
struct Segment {
    first: Seq,
    last: Option<Seq>,
    bytes: u64,
    oldest: Option<SystemTime>,
    newest: Option<SystemTime>,
}

impl Segment {
    fn empty(first: Seq) -> Self {
        Self {
            first,
            last: None,
            bytes: MAGIC_LEN,
            oldest: None,
            newest: None,
        }
    }

    /// Number of records in this segment with a sequence number above `acked`.
    fn records_after(&self, acked: Seq) -> u64 {
        match self.last {
            Some(last) if last > acked => last.get() - self.first.max(acked.next()).get() + 1,
            _ => 0,
        }
    }

    fn next_seq(&self) -> Seq {
        self.last.map_or(self.first, Seq::next)
    }
}

/// Where the last read stopped, so that sequential reads don't rescan the segment.
#[derive(Debug, Clone, Copy)]
struct ReadCursor {
    segment: Seq,
    offset: u64,
    next: Seq,
}

/// The write-ahead log. Not thread-safe; the transport owns it.
#[derive(Debug)]
pub struct Wal {
    dir: PathBuf,
    config: WalConfig,
    _lock: File,
    ack_file: File,
    segments: VecDeque<Segment>,
    active: File,
    next: Seq,
    acked: Seq,
    total_bytes: u64,
    dropped: u64,
    syncs: u64,
    unsynced: bool,
    last_sync: Option<SystemTime>,
    cursor: Option<ReadCursor>,
    active_damaged: bool,
}

impl Wal {
    /// Opens (or creates) the log in `dir`, repairs it after a crash and locks it.
    ///
    /// A new log starts its sequence numbers at the current time in microseconds. If the
    /// directory is ever lost, the numbers of a new log are therefore still higher than all
    /// numbers used before, and the server's deduplication never mistakes new data for old.
    ///
    /// # Errors
    ///
    /// [`WalError::Locked`] if another process uses `dir`, [`WalError::InvalidConfig`],
    /// [`WalError::Io`].
    pub fn open(
        dir: &Path,
        config: WalConfig,
        now: SystemTime,
    ) -> Result<(Self, Recovery), WalError> {
        config.validate()?;
        DirBuilder::new()
            .recursive(true)
            .mode(0o700)
            .create(dir)
            .map_err(io_at(dir))?;
        let lock = lock_dir(dir)?;
        let ack_path = dir.join(ACK_FILE);
        let ack_file = OpenOptions::new()
            .read(true)
            .write(true)
            .create(true)
            .truncate(false)
            .mode(0o600)
            .open(&ack_path)
            .map_err(io_at(&ack_path))?;

        let mut recovery = Recovery::default();
        let mut high_water = Seq::default();
        let mut segments = VecDeque::new();
        for (first, path) in list_segments(dir)? {
            high_water = high_water.max(first);
            let previous_end = segments.back().map(Segment::next_seq);
            if previous_end.is_some_and(|end| first < end) {
                remove_file(&path)?;
                recovery.removed.push(path);
                continue;
            }
            match scan_segment(&path, first, config.max_record_bytes)? {
                Scan::Unreadable => {
                    remove_file(&path)?;
                    recovery.removed.push(path);
                }
                Scan::Valid { segment, cut } => {
                    if cut > 0 {
                        recovery.truncated.push((path, cut));
                    }
                    high_water = high_water.max(segment.next_seq());
                    segments.push_back(segment);
                }
            }
        }

        let next = if segments.is_empty() && high_water == Seq::default() {
            Seq::new(unix_micros(now).max(1))
        } else {
            high_water
        };
        if segments
            .back()
            .is_none_or(|active| active.next_seq() != next)
        {
            create_segment(dir, next, config.fsync)?;
            segments.push_back(Segment::empty(next));
        }
        let first_available = segments.front().map_or(next, |segment| segment.first);
        let before_first = Seq::new(first_available.get().saturating_sub(1));
        let last = Seq::new(next.get().saturating_sub(1));
        let acked =
            read_ack(&ack_file).map_or(before_first, |acked| acked.max(before_first).min(last));

        let active_path = segment_path(dir, next_active_first(&segments));

        let active = OpenOptions::new()
            .append(true)
            .open(&active_path)
            .map_err(io_at(&active_path))?;
        recovery.segments = segments.len();
        recovery.records = segments
            .iter()
            .map(|segment| segment.records_after(Seq::default()))
            .sum();
        let total_bytes = segments.iter().map(|segment| segment.bytes).sum();

        let mut wal = Self {
            dir: dir.to_owned(),
            config,
            _lock: lock,
            ack_file,
            segments,
            active,
            next,
            acked,
            total_bytes,
            dropped: 0,
            syncs: 0,
            unsynced: false,
            last_sync: None,
            cursor: None,
            active_damaged: false,
        };
        wal.remove_acknowledged()?;
        Ok((wal, recovery))
    }

    /// The limits the log was opened with.
    #[must_use]
    pub fn config(&self) -> &WalConfig {
        &self.config
    }

    /// Appends `payload` written at `now` and returns its sequence number. Applies the byte and
    /// age limits first (dropping the oldest segments) and fsyncs according to the policy.
    ///
    /// # Errors
    ///
    /// [`WalError::RecordTooLarge`] or [`WalError::Io`]. A failed write leaves no partial record
    /// behind for later appends.
    pub fn append(&mut self, payload: &[u8], now: SystemTime) -> Result<Seq, WalError> {
        let len = u64::try_from(payload.len()).unwrap_or(u64::MAX);
        if len > self.config.max_record_bytes {
            return Err(WalError::RecordTooLarge {
                len,
                max: self.config.max_record_bytes,
            });
        }
        let record_len = format::RECORD_HEADER_LEN + len;
        self.rotate_if_needed(now)?;
        self.apply_limits(now, record_len)?;

        let seq = self.next;
        let bytes = format::encode(seq, now, payload);
        let path = self.active_path();
        let before = self.active_segment().bytes;
        if let Err(error) = self.active.write_all(&bytes) {
            // Never append behind a partial record: cut it off, or switch segments next time.
            if self.active.set_len(before).is_err() {
                self.active_damaged = true;
            }
            return Err(io_at(&path)(error));
        }
        let segment = self.active_segment_mut();
        segment.last = Some(seq);
        segment.bytes += record_len;
        segment.oldest.get_or_insert(now);
        segment.newest = Some(segment.newest.map_or(now, |newest| newest.max(now)));
        self.total_bytes += record_len;
        self.next = seq.next();
        self.unsynced = true;
        self.sync_per_policy(now)?;
        Ok(seq)
    }

    /// Records the server's acknowledgement of everything up to `seq` (inclusive) and deletes
    /// segments that are completely acknowledged.
    ///
    /// # Errors
    ///
    /// [`WalError::AckBeyondEnd`] if `seq` was never appended (nothing changes), [`WalError::Io`].
    pub fn ack(&mut self, seq: Seq) -> Result<(), WalError> {
        let last = Seq::new(self.next.get().saturating_sub(1));
        if seq > last {
            return Err(WalError::AckBeyondEnd { ack: seq, last });
        }
        if seq <= self.acked {
            return Ok(());
        }
        self.acked = seq;
        let path = self.dir.join(ACK_FILE);
        self.ack_file
            .write_all_at(&encode_ack(seq), 0)
            .map_err(io_at(&path))?;
        self.unsynced = true;
        self.remove_acknowledged()
    }

    /// Up to `max_records` records (and about `max_bytes`, but at least one) with a sequence
    /// number above `after` that are not acknowledged yet, in order.
    ///
    /// # Errors
    ///
    /// [`WalError::Corrupt`] if a segment was damaged after it was opened, [`WalError::Io`].
    pub fn read_after(
        &mut self,
        after: Seq,
        max_records: usize,
        max_bytes: u64,
    ) -> Result<Vec<Record>, WalError> {
        let mut wanted = after.max(self.acked).next();
        let mut records = Vec::new();
        let mut bytes = 0;
        let start = self
            .segments
            .iter()
            .position(|segment| segment.last.is_some_and(|last| last >= wanted));
        let Some(start) = start else {
            return Ok(records);
        };
        for index in start..self.segments.len() {
            let segment = self.segments[index].clone();
            let Some(last) = segment.last else { continue };
            // Gaps between segments (dropped or cut data) are skipped.
            wanted = wanted.max(segment.first);
            let path = segment_path(&self.dir, segment.first);
            let mut offset = match self.cursor {
                Some(cursor) if cursor.segment == segment.first && cursor.next == wanted => {
                    cursor.offset
                }
                _ => MAGIC_LEN,
            };
            let mut file = File::open(&path).map_err(io_at(&path))?;
            file.seek(SeekFrom::Start(offset)).map_err(io_at(&path))?;
            let mut reader = BufReader::new(file.take(segment.bytes.saturating_sub(offset)));
            while wanted <= last {
                if records.len() >= max_records || (bytes >= max_bytes && !records.is_empty()) {
                    return Ok(records);
                }
                let record = match format::read_record(&mut reader, self.config.max_record_bytes) {
                    Ok(Some(record)) => record,
                    Ok(None) => return Err(corrupt(&path, offset, Corruption::Truncated)),
                    Err(ReadError::Corrupt(reason)) => return Err(corrupt(&path, offset, reason)),
                    Err(ReadError::Io(error)) => return Err(io_at(&path)(error)),
                };
                offset += record.encoded_len();
                if record.seq < wanted {
                    continue;
                }
                if record.seq != wanted {
                    return Err(corrupt(
                        &path,
                        offset - record.encoded_len(),
                        Corruption::OutOfSequence,
                    ));
                }
                bytes += record.encoded_len();
                wanted = wanted.next();
                self.cursor = Some(ReadCursor {
                    segment: segment.first,
                    offset,
                    next: wanted,
                });
                records.push(record);
            }
        }
        Ok(records)
    }

    /// Applies the age limit without appending (call periodically, e.g. while the server is
    /// unreachable and nothing is collected).
    ///
    /// # Errors
    ///
    /// [`WalError::Io`].
    pub fn expire(&mut self, now: SystemTime) -> Result<(), WalError> {
        self.rotate_if_needed(now)?;
        self.apply_limits(now, 0)
    }

    /// Forces appended data and the acknowledgement to disk.
    ///
    /// # Errors
    ///
    /// [`WalError::Io`].
    pub fn sync(&mut self, now: SystemTime) -> Result<(), WalError> {
        if !self.unsynced {
            return Ok(());
        }
        let path = self.active_path();
        self.active.sync_data().map_err(io_at(&path))?;
        let ack_path = self.dir.join(ACK_FILE);
        self.ack_file.sync_data().map_err(io_at(&ack_path))?;
        self.syncs += 1;
        self.unsynced = false;
        self.last_sync = Some(now);
        Ok(())
    }

    /// Current counters.
    #[must_use]
    pub fn stats(&self) -> WalStats {
        WalStats {
            next_seq: self.next,
            acked: self.acked,
            pending_records: self
                .segments
                .iter()
                .map(|segment| segment.records_after(self.acked))
                .sum(),
            bytes: self.total_bytes,
            segments: self.segments.len(),
            dropped_records: self.dropped,
            syncs: self.syncs,
        }
    }

    fn sync_per_policy(&mut self, now: SystemTime) -> Result<(), WalError> {
        let due = match self.config.fsync {
            FsyncPolicy::Always => true,
            FsyncPolicy::Never => false,
            FsyncPolicy::Interval(interval) => self
                .last_sync
                .is_none_or(|last| elapsed(last, now) >= interval),
        };
        if due { self.sync(now) } else { Ok(()) }
    }

    fn rotate_if_needed(&mut self, now: SystemTime) -> Result<(), WalError> {
        let active = self.active_segment();
        if active.last.is_none() {
            if self.active_damaged {
                let path = self.active_path();
                self.active.set_len(MAGIC_LEN).map_err(io_at(&path))?;
                self.active_damaged = false;
            }
            return Ok(());
        }
        let full = active.bytes >= self.config.segment_bytes;
        let old = active
            .oldest
            .is_some_and(|oldest| elapsed(oldest, now) >= self.config.segment_span);
        let expired = active
            .newest
            .is_some_and(|newest| elapsed(newest, now) > self.config.max_age);
        if full || old || expired || self.active_damaged {
            self.rotate()?;
        }
        Ok(())
    }

    fn rotate(&mut self) -> Result<(), WalError> {
        if self.config.fsync != FsyncPolicy::Never {
            let path = self.active_path();
            self.active.sync_data().map_err(io_at(&path))?;
        }
        let active = create_segment(&self.dir, self.next, self.config.fsync)?;
        self.segments.push_back(Segment::empty(self.next));
        self.total_bytes += MAGIC_LEN;
        self.active = active;
        self.active_damaged = false;
        Ok(())
    }

    /// Drops the oldest closed segments while the new record wouldn't fit or they are expired.
    fn apply_limits(&mut self, now: SystemTime, incoming: u64) -> Result<(), WalError> {
        while self.segments.len() > 1 {
            let Some(oldest) = self.segments.front() else {
                break;
            };
            let too_big = self.total_bytes + incoming > self.config.max_bytes;
            let expired = oldest
                .newest
                .is_none_or(|newest| elapsed(newest, now) > self.config.max_age);
            if !too_big && !expired {
                break;
            }
            self.dropped += oldest.records_after(self.acked);
            self.remove_oldest()?;
        }
        Ok(())
    }

    fn remove_acknowledged(&mut self) -> Result<(), WalError> {
        while self.segments.len() > 1
            && self
                .segments
                .front()
                .is_some_and(|oldest| oldest.last.is_none_or(|last| last <= self.acked))
        {
            self.remove_oldest()?;
        }
        Ok(())
    }

    fn remove_oldest(&mut self) -> Result<(), WalError> {
        let Some(oldest) = self.segments.pop_front() else {
            return Ok(());
        };
        remove_file(&segment_path(&self.dir, oldest.first))?;
        self.total_bytes -= oldest.bytes;
        if self
            .cursor
            .is_some_and(|cursor| cursor.segment == oldest.first)
        {
            self.cursor = None;
        }
        if self.config.fsync != FsyncPolicy::Never {
            sync_dir(&self.dir)?;
        }
        Ok(())
    }

    fn active_segment(&self) -> &Segment {
        // Invariant: there is always an active segment (created in open, never removed).
        self.segments
            .back()
            .unwrap_or_else(|| unreachable_segment())
    }

    fn active_segment_mut(&mut self) -> &mut Segment {
        self.segments
            .back_mut()
            .unwrap_or_else(|| unreachable_segment())
    }

    fn active_path(&self) -> PathBuf {
        segment_path(&self.dir, self.active_segment().first)
    }
}

#[allow(
    clippy::panic,
    reason = "broken internal invariant, not reachable from outside"
)]
fn unreachable_segment() -> ! {
    panic!("write-ahead log without active segment")
}

fn next_active_first(segments: &VecDeque<Segment>) -> Seq {
    segments
        .back()
        .map_or(Seq::default(), |segment| segment.first)
}

enum Scan {
    Unreadable,
    Valid { segment: Segment, cut: u64 },
}

/// Reads a segment completely, cuts it at the first damaged record and returns its metadata.
fn scan_segment(path: &Path, first: Seq, max_record: u64) -> Result<Scan, WalError> {
    let mut file = OpenOptions::new()
        .read(true)
        .write(true)
        .open(path)
        .map_err(io_at(path))?;
    let length = file.metadata().map_err(io_at(path))?.len();
    let mut reader = BufReader::new(&mut file);
    match format::read_magic(&mut reader) {
        Ok(()) => {}
        Err(ReadError::Corrupt(_)) => return Ok(Scan::Unreadable),
        Err(ReadError::Io(error)) => return Err(io_at(path)(error)),
    }
    let mut segment = Segment::empty(first);
    loop {
        match format::read_record(&mut reader, max_record) {
            Ok(Some(record)) if record.seq == segment.next_seq() => {
                segment.last = Some(record.seq);
                segment.bytes += record.encoded_len();
                segment.oldest.get_or_insert(record.written_at);
                segment.newest = Some(
                    segment
                        .newest
                        .map_or(record.written_at, |newest| newest.max(record.written_at)),
                );
            }
            // End of data, a damaged record or one out of sequence: everything from here is cut.
            Ok(None | Some(_)) | Err(ReadError::Corrupt(_)) => break,
            Err(ReadError::Io(error)) => return Err(io_at(path)(error)),
        }
    }
    drop(reader);
    let cut = length - segment.bytes;
    if cut > 0 {
        file.set_len(segment.bytes).map_err(io_at(path))?;
        file.sync_all().map_err(io_at(path))?;
    }
    Ok(Scan::Valid { segment, cut })
}

fn list_segments(dir: &Path) -> Result<Vec<(Seq, PathBuf)>, WalError> {
    let mut segments = Vec::new();
    for entry in fs::read_dir(dir).map_err(io_at(dir))? {
        let entry = entry.map_err(io_at(dir))?;
        let name = entry.file_name();
        let Some(first) = name
            .to_str()
            .and_then(|name| name.strip_prefix(SEGMENT_PREFIX))
            .and_then(|rest| rest.strip_suffix(SEGMENT_SUFFIX))
            .filter(|digits| digits.len() == 20 && digits.bytes().all(|byte| byte.is_ascii_digit()))
            .and_then(|digits| digits.parse().ok())
        else {
            continue;
        };
        if entry.file_type().map_err(io_at(dir))?.is_file() {
            segments.push((Seq::new(first), entry.path()));
        }
    }
    segments.sort();
    Ok(segments)
}

fn create_segment(dir: &Path, first: Seq, fsync: FsyncPolicy) -> Result<File, WalError> {
    let path = segment_path(dir, first);
    let mut file = OpenOptions::new()
        .append(true)
        .create_new(true)
        .mode(0o600)
        .open(&path)
        .map_err(io_at(&path))?;
    file.write_all(MAGIC).map_err(io_at(&path))?;
    if fsync != FsyncPolicy::Never {
        file.sync_data().map_err(io_at(&path))?;
        sync_dir(dir)?;
    }
    Ok(file)
}

fn segment_path(dir: &Path, first: Seq) -> PathBuf {
    dir.join(format!(
        "{SEGMENT_PREFIX}{:020}{SEGMENT_SUFFIX}",
        first.get()
    ))
}

fn lock_dir(dir: &Path) -> Result<File, WalError> {
    let path = dir.join(LOCK_FILE);
    let file = OpenOptions::new()
        .write(true)
        .create(true)
        .truncate(false)
        .mode(0o600)
        .open(&path)
        .map_err(io_at(&path))?;
    match file.try_lock() {
        Ok(()) => Ok(file),
        Err(TryLockError::WouldBlock) => Err(WalError::Locked(dir.to_owned())),
        Err(TryLockError::Error(error)) => Err(io_at(&path)(error)),
    }
}

fn encode_ack(seq: Seq) -> [u8; 12] {
    let mut bytes = [0; 12];
    bytes[..8].copy_from_slice(&seq.get().to_le_bytes());
    let crc = crc32fast::hash(&bytes[..8]);
    bytes[8..].copy_from_slice(&crc.to_le_bytes());
    bytes
}

fn read_ack(file: &File) -> Option<Seq> {
    let mut bytes = [0; 12];
    file.read_exact_at(&mut bytes, 0).ok()?;
    let mut seq = [0; 8];
    seq.copy_from_slice(&bytes[..8]);
    let mut crc = [0; 4];
    crc.copy_from_slice(&bytes[8..]);
    (crc32fast::hash(&seq) == u32::from_le_bytes(crc)).then(|| Seq::new(u64::from_le_bytes(seq)))
}

fn remove_file(path: &Path) -> Result<(), WalError> {
    match fs::remove_file(path) {
        Err(error) if error.kind() != io::ErrorKind::NotFound => Err(io_at(path)(error)),
        _ => Ok(()),
    }
}

fn sync_dir(dir: &Path) -> Result<(), WalError> {
    File::open(dir)
        .and_then(|handle| handle.sync_all())
        .map_err(io_at(dir))
}

/// Time from `earlier` to `later`; zero if the clock went backwards.
fn elapsed(earlier: SystemTime, later: SystemTime) -> Duration {
    later.duration_since(earlier).unwrap_or(Duration::ZERO)
}

fn unix_micros(now: SystemTime) -> u64 {
    let since = now.duration_since(UNIX_EPOCH).unwrap_or(Duration::ZERO);
    u64::try_from(since.as_micros()).unwrap_or(u64::MAX)
}

fn corrupt(path: &Path, offset: u64, reason: Corruption) -> WalError {
    WalError::Corrupt {
        path: path.to_owned(),
        offset,
        reason,
    }
}

fn io_at(path: &Path) -> impl Fn(io::Error) -> WalError + '_ {
    move |source| WalError::Io {
        path: path.to_owned(),
        source,
    }
}

#[cfg(test)]
mod tests {
    use std::os::unix::fs::PermissionsExt;

    use super::*;

    const START: Duration = Duration::from_hours(498_624);

    fn t(seconds: u64) -> SystemTime {
        UNIX_EPOCH + START + Duration::from_secs(seconds)
    }

    fn small() -> WalConfig {
        WalConfig {
            max_bytes: 4096,
            max_age: Duration::from_hours(1),
            segment_bytes: 256,
            segment_span: Duration::from_mins(10),
            max_record_bytes: 64,
            fsync: FsyncPolicy::Never,
        }
    }

    fn open(dir: &Path, config: WalConfig, now: SystemTime) -> (Wal, Recovery) {
        Wal::open(dir, config, now).unwrap()
    }

    fn payloads(records: &[Record]) -> Vec<Vec<u8>> {
        records
            .iter()
            .map(|record| record.payload.clone())
            .collect()
    }

    fn segment_files(dir: &Path) -> Vec<PathBuf> {
        list_segments(dir)
            .unwrap()
            .into_iter()
            .map(|(_, path)| path)
            .collect()
    }

    #[test]
    fn a_new_log_starts_at_the_current_time_in_microseconds() {
        let dir = tempfile::tempdir().unwrap();
        let (mut wal, recovery) = open(dir.path(), small(), t(0));

        let seq = wal.append(b"a", t(0)).unwrap();

        assert_eq!(seq, Seq::new(unix_micros(t(0))));
        assert_eq!(wal.stats().acked, Seq::new(seq.get() - 1));
        assert_eq!(recovery.segments, 1);
    }

    #[test]
    fn reads_unacknowledged_records_in_order_within_limits() {
        let dir = tempfile::tempdir().unwrap();
        let (mut wal, _) = open(dir.path(), small(), t(0));
        let first = wal.append(b"one", t(0)).unwrap();
        wal.append(b"two", t(1)).unwrap();
        wal.append(b"three", t(2)).unwrap();

        let all = wal.read_after(Seq::default(), 10, u64::MAX).unwrap();
        assert_eq!(
            payloads(&all),
            vec![b"one".to_vec(), b"two".to_vec(), b"three".to_vec()]
        );
        assert_eq!(all[1].seq, first.next());
        assert_eq!(all[2].written_at, t(2));

        assert_eq!(
            payloads(&wal.read_after(first, 1, u64::MAX).unwrap()),
            vec![b"two".to_vec()]
        );
        // A byte limit below one record still returns one record.
        assert_eq!(wal.read_after(first, 10, 1).unwrap().len(), 1);
    }

    #[test]
    fn acknowledged_records_are_never_read_again_and_closed_segments_are_deleted() {
        let dir = tempfile::tempdir().unwrap();
        let (mut wal, _) = open(dir.path(), small(), t(0));
        let mut seqs = Vec::new();
        for i in 0..20 {
            seqs.push(wal.append(&[i; 40], t(0)).unwrap());
        }
        assert!(wal.stats().segments > 1);

        wal.ack(seqs[14]).unwrap();

        let rest = wal.read_after(Seq::default(), 100, u64::MAX).unwrap();
        assert_eq!(rest.first().map(|record| record.seq), Some(seqs[15]));
        assert_eq!(wal.stats().pending_records, 5);
        assert!(segment_files(dir.path()).len() < 4);

        wal.ack(seqs[3]).unwrap(); // older ack: no effect
        assert_eq!(wal.stats().acked, seqs[14]);
        assert!(matches!(
            wal.ack(seqs[19].next()),
            Err(WalError::AckBeyondEnd { .. })
        ));
    }

    #[test]
    fn restart_keeps_sequence_and_acknowledgement() {
        let dir = tempfile::tempdir().unwrap();
        let (mut wal, _) = open(dir.path(), small(), t(0));
        let a = wal.append(b"a", t(0)).unwrap();
        let b = wal.append(b"b", t(0)).unwrap();
        let c = wal.append(b"c", t(0)).unwrap();
        wal.ack(b).unwrap();
        drop(wal);

        let (mut wal, recovery) = open(dir.path(), small(), t(100));

        assert_eq!(recovery.records, 3);
        assert_eq!(wal.stats().acked, b);
        let rest = wal.read_after(a, 10, u64::MAX).unwrap();
        assert_eq!(
            rest.iter().map(|record| record.seq).collect::<Vec<_>>(),
            vec![c]
        );
        assert_eq!(wal.append(b"d", t(100)).unwrap(), c.next());
    }

    #[test]
    fn sequence_survives_when_everything_was_acknowledged() {
        let dir = tempfile::tempdir().unwrap();
        let (mut wal, _) = open(dir.path(), small(), t(0));
        let mut last = Seq::default();
        for _ in 0..30 {
            last = wal.append(&[7; 40], t(0)).unwrap();
        }
        wal.ack(last).unwrap();
        wal.append(&[8; 60], t(0)).unwrap();
        let after_rotation = wal.append(&[9; 60], t(0)).unwrap();
        wal.ack(after_rotation).unwrap();
        drop(wal);

        // Later and with a clock that went backwards: the counter continues, it doesn't restart.
        let (mut wal, _) = open(dir.path(), small(), t(0) - Duration::from_hours(48));

        assert_eq!(wal.append(b"x", t(0)).unwrap(), after_rotation.next());
    }

    #[test]
    fn a_second_process_cannot_open_the_same_log() {
        let dir = tempfile::tempdir().unwrap();
        let (_wal, _) = open(dir.path(), small(), t(0));

        let error = Wal::open(dir.path(), small(), t(0)).unwrap_err();

        assert!(matches!(error, WalError::Locked(_)), "{error}");
    }

    #[test]
    fn files_are_private() {
        let dir = tempfile::tempdir().unwrap();
        let wal_dir = dir.path().join("wal");
        let (mut wal, _) = open(&wal_dir, small(), t(0));
        wal.append(b"secret log line", t(0)).unwrap();
        let mode = |path: &Path| fs::metadata(path).unwrap().permissions().mode() & 0o777;

        assert_eq!(mode(&wal_dir), 0o700);
        for path in segment_files(&wal_dir) {
            assert_eq!(mode(&path), 0o600);
        }
        assert_eq!(mode(&wal_dir.join(ACK_FILE)), 0o600);
    }

    #[test]
    fn rotates_by_size_and_by_span() {
        let dir = tempfile::tempdir().unwrap();
        let (mut wal, _) = open(dir.path(), small(), t(0));
        for _ in 0..4 {
            wal.append(&[1; 60], t(0)).unwrap(); // 84 bytes each: the 4th starts a new segment
        }
        assert_eq!(wal.stats().segments, 2);

        wal.append(b"later", t(11 * 60)).unwrap();

        assert_eq!(wal.stats().segments, 3);
    }

    #[test]
    fn drops_the_oldest_segments_at_the_byte_limit_and_counts_them() {
        let dir = tempfile::tempdir().unwrap();
        let (mut wal, _) = open(dir.path(), small(), t(0));
        let first = wal.append(&[0; 64], t(0)).unwrap();
        for _ in 0..200 {
            wal.append(&[0; 64], t(0)).unwrap();
        }

        let stats = wal.stats();
        assert!(stats.bytes <= small().max_bytes, "{stats:?}");
        assert!(stats.dropped_records > 0);
        assert_eq!(stats.dropped_records + stats.pending_records, 201);
        let oldest = wal.read_after(Seq::default(), 1, u64::MAX).unwrap();
        assert!(oldest[0].seq > first);
    }

    #[test]
    fn drops_expired_data_also_without_new_appends() {
        let dir = tempfile::tempdir().unwrap();
        let (mut wal, _) = open(dir.path(), small(), t(0));
        wal.append(b"old", t(0)).unwrap();

        wal.expire(t(3601)).unwrap();

        assert_eq!(wal.stats().pending_records, 0);
        assert_eq!(wal.stats().dropped_records, 1);
        assert_eq!(
            wal.read_after(Seq::default(), 10, u64::MAX).unwrap(),
            vec![]
        );
    }

    #[test]
    fn cuts_a_torn_write_at_the_end_and_continues() {
        let dir = tempfile::tempdir().unwrap();
        let (mut wal, _) = open(dir.path(), small(), t(0));
        let a = wal.append(b"complete", t(0)).unwrap();
        let b = wal.append(b"torn", t(0)).unwrap();
        drop(wal);
        let active = segment_files(dir.path()).pop().unwrap();
        let length = fs::metadata(&active).unwrap().len();
        OpenOptions::new()
            .write(true)
            .open(&active)
            .unwrap()
            .set_len(length - 3)
            .unwrap();

        let (mut wal, recovery) = open(dir.path(), small(), t(1));

        assert_eq!(recovery.truncated, vec![(active, 25)]);
        assert_eq!(
            payloads(&wal.read_after(Seq::default(), 10, u64::MAX).unwrap()),
            vec![b"complete".to_vec()]
        );
        // The lost number is reused: the server never acknowledged it.
        assert_eq!(wal.append(b"next", t(1)).unwrap(), b);
        assert_eq!(a.next(), b);
        assert_eq!(
            payloads(&wal.read_after(Seq::default(), 10, u64::MAX).unwrap()),
            vec![b"complete".to_vec(), b"next".to_vec()]
        );
    }

    #[test]
    fn damage_in_an_older_segment_keeps_the_newer_ones() {
        let dir = tempfile::tempdir().unwrap();
        let (mut wal, _) = open(dir.path(), small(), t(0));
        for i in 0..10 {
            wal.append(&[i; 60], t(0)).unwrap();
        }
        drop(wal);
        let first = segment_files(dir.path()).remove(0);
        let mut bytes = fs::read(&first).unwrap();
        bytes[MAGIC.len() + 84 + 30] ^= 0xff; // inside the second record
        fs::write(&first, bytes).unwrap();

        let (mut wal, recovery) = open(dir.path(), small(), t(1));

        assert_eq!(recovery.truncated.len(), 1);
        let records = wal.read_after(Seq::default(), 100, u64::MAX).unwrap();
        assert_eq!(records[0].payload, vec![0; 60]);
        assert_eq!(
            records[1].payload,
            vec![3; 60],
            "segment 2 starts with record 4"
        );
        assert_eq!(records.len(), 8);
    }

    #[test]
    fn unreadable_segments_are_removed_and_foreign_files_ignored() {
        let dir = tempfile::tempdir().unwrap();
        let (mut wal, _) = open(dir.path(), small(), t(0));
        let seq = wal.append(b"a", t(0)).unwrap();
        drop(wal);
        let bogus = segment_path(dir.path(), Seq::new(seq.get() + 1000));
        fs::write(&bogus, b"not a segment").unwrap();
        fs::write(dir.path().join("notes.txt"), b"keep me").unwrap();

        let (mut wal, recovery) = open(dir.path(), small(), t(1));

        assert_eq!(recovery.removed, vec![bogus]);
        assert!(dir.path().join("notes.txt").exists());
        // Numbers seen in file names are never reused.
        assert_eq!(wal.append(b"b", t(1)).unwrap(), Seq::new(seq.get() + 1000));
    }

    #[test]
    fn fsync_follows_the_policy() {
        let cases = [
            (FsyncPolicy::Always, 3),
            (FsyncPolicy::Interval(Duration::from_secs(5)), 2), // at 0 s and at 5 s
            (FsyncPolicy::Never, 0),
        ];
        for (policy, expected) in cases {
            let dir = tempfile::tempdir().unwrap();
            let config = WalConfig {
                fsync: policy,
                ..small()
            };
            let (mut wal, _) = open(dir.path(), config, t(0));

            for second in [0, 2, 5] {
                wal.append(b"x", t(second)).unwrap();
            }

            assert_eq!(wal.stats().syncs, expected, "{policy:?}");
        }
    }

    #[test]
    fn refuses_records_above_the_limit() {
        let dir = tempfile::tempdir().unwrap();
        let (mut wal, _) = open(dir.path(), small(), t(0));

        let error = wal.append(&[0; 65], t(0)).unwrap_err();

        assert!(matches!(
            error,
            WalError::RecordTooLarge { len: 65, max: 64 }
        ));
        assert_eq!(wal.stats().pending_records, 0);
    }
}
