//! Property tests of the write-ahead log against a simple model: order, acknowledgement,
//! restarts, limits and crash recovery.

// Test code: panicking on setup failures is the desired behaviour.
#![allow(clippy::unwrap_used, clippy::expect_used, clippy::panic)]

use std::collections::BTreeMap;
use std::path::Path;
use std::time::{Duration, UNIX_EPOCH};

use proptest::prelude::*;
use sermon_buffer::{FsyncPolicy, Record, Seq, Wal, WalConfig};

const START: Duration = Duration::from_hours(498_624);

fn config() -> WalConfig {
    WalConfig {
        max_bytes: 2048,
        max_age: Duration::from_hours(1),
        segment_bytes: 256,
        segment_span: Duration::from_mins(10),
        max_record_bytes: 64,
        fsync: FsyncPolicy::Never,
    }
}

fn generous() -> WalConfig {
    WalConfig {
        max_bytes: 16 * 1024 * 1024,
        max_age: Duration::from_hours(24),
        ..config()
    }
}

fn read_all(wal: &mut Wal) -> Vec<Record> {
    wal.read_after(Seq::default(), usize::MAX, u64::MAX)
        .unwrap()
}

fn assert_ascending_and_contiguous(records: &[Record]) {
    for pair in records.windows(2) {
        assert_eq!(pair[1].seq, pair[0].seq.next(), "gap or reordering");
    }
}

fn dir_bytes(dir: &Path) -> u64 {
    std::fs::read_dir(dir)
        .unwrap()
        .map(|entry| entry.unwrap())
        .filter(|entry| entry.file_name().to_string_lossy().starts_with("seg-"))
        .map(|entry| entry.metadata().unwrap().len())
        .sum()
}

#[derive(Debug, Clone)]
enum Op {
    Append(Vec<u8>),
    /// Acknowledge the n-th of the records appended so far (modulo their number).
    Ack(usize),
    /// Read after the n-th record (modulo, or from the start) with a record limit.
    Read {
        after: Option<usize>,
        max: usize,
    },
    Reopen,
    Advance(u64),
}

fn op() -> impl Strategy<Value = Op> {
    prop_oneof![
        4 => proptest::collection::vec(any::<u8>(), 0..=64).prop_map(Op::Append),
        2 => any::<usize>().prop_map(Op::Ack),
        2 => (proptest::option::of(any::<usize>()), 1..10_usize)
            .prop_map(|(after, max)| Op::Read { after, max }),
        1 => Just(Op::Reopen),
        1 => (0..120_u64).prop_map(Op::Advance),
    ]
}

proptest! {
    #![proptest_config(ProptestConfig::with_cases(200))]

    /// Without limits the log behaves like an ordered queue: records come back in order with
    /// their payload, acknowledged records never come back (also not after a restart), and the
    /// sequence numbers continue across restarts.
    #[test]
    fn behaves_like_an_ordered_queue(ops in proptest::collection::vec(op(), 1..80)) {
        let dir = tempfile::tempdir().unwrap();
        let mut now = UNIX_EPOCH + START;
        let (mut wal, _) = Wal::open(dir.path(), generous(), now).unwrap();
        let mut model: Vec<(Seq, Vec<u8>)> = Vec::new();
        let mut acked: Option<Seq> = None;

        for op in ops {
            match op {
                Op::Append(payload) => {
                    let seq = wal.append(&payload, now).unwrap();
                    if let Some((last, _)) = model.last() {
                        prop_assert_eq!(seq, last.next());
                    }
                    model.push((seq, payload));
                }
                Op::Ack(index) if !model.is_empty() => {
                    let seq = model[index % model.len()].0;
                    wal.ack(seq).unwrap();
                    acked = acked.max(Some(seq));
                }
                Op::Ack(_) => {}
                Op::Read { after, max } => {
                    let after = after
                        .filter(|_| !model.is_empty())
                        .map_or(Seq::default(), |index| model[index % model.len()].0);
                    let records = wal.read_after(after, max, u64::MAX).unwrap();
                    let floor = acked.unwrap_or_default().max(after);
                    let expected: Vec<&(Seq, Vec<u8>)> =
                        model.iter().filter(|(seq, _)| *seq > floor).take(max).collect();
                    prop_assert_eq!(records.len(), expected.len());
                    for (record, (seq, payload)) in records.iter().zip(expected) {
                        prop_assert_eq!(record.seq, *seq);
                        prop_assert_eq!(&record.payload, payload);
                    }
                }
                Op::Reopen => {
                    drop(wal);
                    wal = Wal::open(dir.path(), generous(), now).unwrap().0;
                    if let Some(acked) = acked {
                        prop_assert_eq!(wal.stats().acked, acked);
                    }
                    if let Some((last, _)) = model.last() {
                        prop_assert_eq!(wal.stats().next_seq, last.next());
                    }
                }
                Op::Advance(seconds) => now += Duration::from_secs(seconds),
            }
        }

        let pending = model.iter().filter(|(seq, _)| Some(*seq) > acked).count();
        prop_assert_eq!(wal.stats().pending_records, u64::try_from(pending).unwrap());
    }

    /// With small limits: never above the byte limit, nothing much older than the age limit,
    /// every append is either still there or counted as dropped, and what remains is the newest
    /// part without gaps.
    #[test]
    fn keeps_its_limits(appends in proptest::collection::vec((0..=64_usize, 0..900_u64), 1..150)) {
        let dir = tempfile::tempdir().unwrap();
        let config = config();
        let mut now = UNIX_EPOCH + START;
        let (mut wal, _) = Wal::open(dir.path(), config.clone(), now).unwrap();
        for (count, (len, advance)) in appends.iter().enumerate() {
            now += Duration::from_secs(*advance);
            let last = wal.append(&vec![0xAB; *len], now).unwrap();

            let stats = wal.stats();
            prop_assert!(stats.bytes <= config.max_bytes, "{:?}", stats);
            prop_assert_eq!(stats.bytes, dir_bytes(dir.path()));
            prop_assert_eq!(stats.pending_records + stats.dropped_records, u64::try_from(count + 1).unwrap());

            let records = read_all(&mut wal);
            assert_ascending_and_contiguous(&records);
            prop_assert_eq!(records.last().map(|record| record.seq), Some(last));
            let oldest_allowed = now - config.max_age - config.segment_span - Duration::from_secs(1);
            for record in &records {
                prop_assert!(record.written_at >= oldest_allowed);
            }
        }
    }

    /// After damage to any segment (cut, flipped byte, trailing garbage) the log opens, returns
    /// only records that were written – unchanged and in order – and keeps numbering upwards.
    #[test]
    fn recovers_from_damage(
        payloads in proptest::collection::vec(proptest::collection::vec(any::<u8>(), 0..=64), 1..40),
        segment_pick in any::<usize>(),
        damage in 0..3_u8,
        position in any::<u64>(),
        garbage in proptest::collection::vec(any::<u8>(), 1..40),
    ) {
        let dir = tempfile::tempdir().unwrap();
        let now = UNIX_EPOCH + START;
        let (mut wal, _) = Wal::open(dir.path(), generous(), now).unwrap();
        let mut written = BTreeMap::new();
        for payload in &payloads {
            written.insert(wal.append(payload, now).unwrap(), payload.clone());
        }
        let last_written = *written.keys().last().unwrap();
        drop(wal);

        let mut segments: Vec<_> = std::fs::read_dir(dir.path())
            .unwrap()
            .map(|entry| entry.unwrap().path())
            .filter(|path| path.file_name().unwrap().to_string_lossy().starts_with("seg-"))
            .collect();
        segments.sort();
        let target = &segments[segment_pick % segments.len()];
        let mut bytes = std::fs::read(target).unwrap();
        let len = u64::try_from(bytes.len()).unwrap();
        match damage {
            0 => bytes.truncate(usize::try_from(position % (len + 1)).unwrap()),
            1 => {
                let index = usize::try_from(position % len).unwrap();
                bytes[index] ^= 0x5A;
            }
            _ => bytes.extend(&garbage),
        }
        std::fs::write(target, bytes).unwrap();

        let (mut wal, _) = Wal::open(dir.path(), generous(), now).unwrap();
        let records = read_all(&mut wal);

        let mut previous = None;
        for record in &records {
            prop_assert_eq!(Some(&record.payload), written.get(&record.seq), "unknown or changed record");
            prop_assert!(previous < Some(record.seq));
            previous = Some(record.seq);
        }
        if damage == 2 && target == segments.last().unwrap() {
            prop_assert_eq!(records.len(), payloads.len(), "garbage at the end loses nothing");
        }
        let next = wal.append(b"after the crash", now).unwrap();
        prop_assert!(Some(next) > previous);
        prop_assert!(next <= last_written.next(), "numbering jumped");

        // New data behind the repaired end is readable, now and after another restart.
        for _ in 0..2 {
            let after = read_all(&mut wal);
            prop_assert_eq!(after.len(), records.len() + 1);
            prop_assert_eq!(&after.last().unwrap().payload, &b"after the crash".to_vec());
            drop(wal);
            wal = Wal::open(dir.path(), generous(), now).unwrap().0;
        }
    }
}
