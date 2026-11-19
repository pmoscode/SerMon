//! On-disk format of a segment: a header followed by records.
//!
//! ```text
//! segment = MAGIC record*
//! record  = len:u32 crc:u32 seq:u64 written_at_ms:u64 payload[len]     (little endian)
//! crc     = CRC-32 over seq, written_at_ms and payload
//! ```
//!
//! The reader trusts nothing: a length above the limit, a short read or a wrong checksum is a
//! [`Corruption`], never a panic or a huge allocation.

use std::io::{self, Read};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use crate::Seq;

/// First bytes of every segment file (format version 1).
pub const MAGIC: &[u8; 8] = b"SMWAL001";
/// Bytes before the payload of a record.
pub const RECORD_HEADER_LEN: u64 = 24;

/// A record as stored.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Record {
    /// Sequence number, unique and increasing.
    pub seq: Seq,
    /// Wall-clock time of the append (for the age limit), millisecond precision.
    pub written_at: SystemTime,
    /// The data, opaque to the buffer.
    pub payload: Vec<u8>,
}

impl Record {
    /// Bytes the record occupies on disk.
    #[must_use]
    pub fn encoded_len(&self) -> u64 {
        RECORD_HEADER_LEN + u64::try_from(self.payload.len()).unwrap_or(u64::MAX)
    }
}

/// Why bytes are not a valid record.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum Corruption {
    /// The data ends inside a record (typical after a crash during a write).
    #[error("record cut off")]
    Truncated,
    /// The length field exceeds the record limit.
    #[error("record length {0} above the limit")]
    TooLong(u64),
    /// The checksum doesn't match.
    #[error("checksum mismatch")]
    Checksum,
    /// The sequence number doesn't continue the previous record.
    #[error("sequence number out of order")]
    OutOfSequence,
    /// The segment doesn't start with [`MAGIC`].
    #[error("not a SerMon WAL segment")]
    BadMagic,
}

/// A read error: either the underlying I/O failed or the data is corrupt.
#[derive(Debug, thiserror::Error)]
pub enum ReadError {
    /// I/O error of the source.
    #[error(transparent)]
    Io(#[from] io::Error),
    /// Invalid data.
    #[error(transparent)]
    Corrupt(#[from] Corruption),
}

/// Encodes a record.
#[must_use]
pub fn encode(seq: Seq, written_at: SystemTime, payload: &[u8]) -> Vec<u8> {
    let millis = to_millis(written_at);
    let mut out = Vec::with_capacity(payload.len() + 24);
    out.extend_from_slice(
        &u32::try_from(payload.len())
            .unwrap_or(u32::MAX)
            .to_le_bytes(),
    );
    out.extend_from_slice(&checksum(seq.get(), millis, payload).to_le_bytes());
    out.extend_from_slice(&seq.get().to_le_bytes());
    out.extend_from_slice(&millis.to_le_bytes());
    out.extend_from_slice(payload);
    out
}

/// Checks the segment header.
///
/// # Errors
///
/// [`Corruption::BadMagic`] for a wrong or short header, [`ReadError::Io`] for I/O errors.
pub fn read_magic(source: &mut impl Read) -> Result<(), ReadError> {
    let mut magic = [0; 8];
    match read_full(source, &mut magic)? {
        8 if &magic == MAGIC => Ok(()),
        _ => Err(Corruption::BadMagic.into()),
    }
}

/// Reads the next record; `Ok(None)` at a clean end of the data.
///
/// # Errors
///
/// [`ReadError::Corrupt`] for invalid data, [`ReadError::Io`] for I/O errors.
pub fn read_record(source: &mut impl Read, max_payload: u64) -> Result<Option<Record>, ReadError> {
    let mut header = [0; 24];
    match read_full(source, &mut header)? {
        0 => return Ok(None),
        24 => {}
        _ => return Err(Corruption::Truncated.into()),
    }
    let len = u64::from(le_u32(&header[0..4]));
    let crc = le_u32(&header[4..8]);
    let seq = le_u64(&header[8..16]);
    let millis = le_u64(&header[16..24]);
    if len > max_payload {
        return Err(Corruption::TooLong(len).into());
    }
    let mut payload = vec![0; usize::try_from(len).map_err(|_| Corruption::TooLong(len))?];
    if read_full(source, &mut payload)? != payload.len() {
        return Err(Corruption::Truncated.into());
    }
    if checksum(seq, millis, &payload) != crc {
        return Err(Corruption::Checksum.into());
    }
    Ok(Some(Record {
        seq: Seq::new(seq),
        written_at: UNIX_EPOCH + Duration::from_millis(millis),
        payload,
    }))
}

fn checksum(seq: u64, millis: u64, payload: &[u8]) -> u32 {
    let mut hasher = crc32fast::Hasher::new();
    hasher.update(&seq.to_le_bytes());
    hasher.update(&millis.to_le_bytes());
    hasher.update(payload);
    hasher.finalize()
}

/// Milliseconds since the epoch; times before the epoch count as the epoch.
fn to_millis(time: SystemTime) -> u64 {
    let since = time.duration_since(UNIX_EPOCH).unwrap_or(Duration::ZERO);
    u64::try_from(since.as_millis()).unwrap_or(u64::MAX)
}

/// Fills `buffer` as far as the source allows; returns the number of bytes read.
fn read_full(source: &mut impl Read, buffer: &mut [u8]) -> io::Result<usize> {
    let mut filled = 0;
    while filled < buffer.len() {
        match source.read(&mut buffer[filled..]) {
            Ok(0) => break,
            Ok(n) => filled += n,
            Err(error) if error.kind() == io::ErrorKind::Interrupted => {}
            Err(error) => return Err(error),
        }
    }
    Ok(filled)
}

fn le_u32(bytes: &[u8]) -> u32 {
    let mut array = [0; 4];
    array.copy_from_slice(bytes);
    u32::from_le_bytes(array)
}

fn le_u64(bytes: &[u8]) -> u64 {
    let mut array = [0; 8];
    array.copy_from_slice(bytes);
    u64::from_le_bytes(array)
}

#[cfg(test)]
mod tests {
    use std::io::Cursor;

    use proptest::prelude::*;

    use super::*;

    const MAX: u64 = 1024;

    fn at(millis: u64) -> SystemTime {
        UNIX_EPOCH + Duration::from_millis(millis)
    }

    fn read_all(bytes: &[u8]) -> (Vec<Record>, Option<Corruption>) {
        let mut cursor = Cursor::new(bytes);
        let mut records = Vec::new();
        loop {
            match read_record(&mut cursor, MAX) {
                Ok(Some(record)) => records.push(record),
                Ok(None) => return (records, None),
                Err(ReadError::Corrupt(corruption)) => return (records, Some(corruption)),
                Err(ReadError::Io(error)) => panic!("{error}"),
            }
        }
    }

    #[test]
    fn records_roundtrip() {
        let mut bytes = encode(Seq::new(7), at(1_000), b"hello");
        bytes.extend(encode(Seq::new(8), at(2_000), b""));

        let (records, corruption) = read_all(&bytes);

        assert_eq!(corruption, None);
        assert_eq!(
            records,
            vec![
                Record {
                    seq: Seq::new(7),
                    written_at: at(1_000),
                    payload: b"hello".to_vec()
                },
                Record {
                    seq: Seq::new(8),
                    written_at: at(2_000),
                    payload: Vec::new()
                },
            ]
        );
        assert_eq!(records[0].encoded_len(), 29);
    }

    #[test]
    fn detects_cut_off_records() {
        let bytes = encode(Seq::new(1), at(1), b"payload");
        for cut in 1..bytes.len() {
            assert_eq!(
                read_all(&bytes[..cut]).1,
                Some(Corruption::Truncated),
                "cut {cut}"
            );
        }
    }

    #[test]
    fn detects_flipped_bits_in_payload_and_header_fields() {
        let bytes = encode(Seq::new(1), at(1), b"payload");
        for index in 4..bytes.len() {
            let mut damaged = bytes.clone();
            damaged[index] ^= 0x01;
            assert_eq!(
                read_all(&damaged).1,
                Some(Corruption::Checksum),
                "byte {index}"
            );
        }
    }

    #[test]
    fn refuses_lengths_above_the_limit_without_allocating() {
        let mut bytes = encode(Seq::new(1), at(1), b"x");
        bytes[0..4].copy_from_slice(&u32::MAX.to_le_bytes());

        assert_eq!(
            read_all(&bytes).1,
            Some(Corruption::TooLong(u64::from(u32::MAX)))
        );
    }

    #[test]
    fn checks_the_segment_magic() {
        assert!(read_magic(&mut Cursor::new(MAGIC)).is_ok());
        for bad in [&b""[..], b"SMWAL", b"SMWAL002", b"XXXXXXXXXXXX"] {
            assert!(matches!(
                read_magic(&mut Cursor::new(bad)),
                Err(ReadError::Corrupt(Corruption::BadMagic))
            ));
        }
    }

    proptest! {
        #![proptest_config(ProptestConfig::with_cases(2_000))]

        /// Fuzzing the read path: arbitrary bytes never panic, and whatever is returned before
        /// the first error has a valid checksum by construction.
        #[test]
        fn arbitrary_bytes_never_panic(bytes in proptest::collection::vec(any::<u8>(), 0..512)) {
            let (records, _) = read_all(&bytes);
            for record in records {
                prop_assert!(u64::try_from(record.payload.len()).unwrap() <= MAX);
            }
        }

        /// Valid records followed by garbage: the valid prefix is always recovered intact.
        #[test]
        fn valid_prefix_survives_trailing_garbage(
            payloads in proptest::collection::vec(proptest::collection::vec(any::<u8>(), 0..64), 0..8),
            garbage in proptest::collection::vec(any::<u8>(), 0..64),
        ) {
            let mut bytes = Vec::new();
            for (index, payload) in payloads.iter().enumerate() {
                bytes.extend(encode(Seq::new(u64::try_from(index).unwrap() + 1), at(u64::try_from(index).unwrap()), payload));
            }
            bytes.extend(&garbage);

            let (records, _) = read_all(&bytes);

            prop_assert!(records.len() >= payloads.len());
            for (record, payload) in records.iter().zip(&payloads) {
                prop_assert_eq!(&record.payload, payload);
            }
        }
    }
}
