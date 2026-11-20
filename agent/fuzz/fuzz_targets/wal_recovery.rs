//! Opening the write-ahead log must survive any segment content (crash, disk errors, tampering):
//! no panic, and the log stays usable – appending and reading work after the recovery.
#![no_main]

use std::time::{Duration, SystemTime};

use libfuzzer_sys::fuzz_target;
use sermon_buffer::{FsyncPolicy, Seq, Wal, WalConfig};

fuzz_target!(|data: &[u8]| {
    let dir = tempfile::tempdir().expect("temp dir");
    let config = WalConfig {
        fsync: FsyncPolicy::Never,
        ..WalConfig::default()
    };
    let now = SystemTime::UNIX_EPOCH + Duration::from_secs(1_800_000_000);
    {
        let (mut wal, _) = Wal::open(dir.path(), config.clone(), now).expect("open empty");
        wal.append(b"seed", now).expect("append");
        wal.sync(now).expect("sync");
    }
    for entry in std::fs::read_dir(dir.path()).expect("list") {
        let path = entry.expect("entry").path();
        if path.is_file() && path.extension().is_some_and(|ext| ext == "wal") {
            std::fs::write(&path, data).expect("overwrite segment");
        }
    }

    let Ok((mut wal, _recovery)) = Wal::open(dir.path(), config.clone(), now) else {
        return;
    };
    let _ = wal.read_after(Seq::new(0), 64, 1 << 20);
    let seq = wal
        .append(b"after recovery", now)
        .expect("append after recovery");
    let records = wal
        .read_after(Seq::new(0), 1024, 16 << 20)
        .expect("read after recovery");
    assert!(
        records.iter().any(|r| r.seq == seq),
        "the new record must be readable"
    );
});
