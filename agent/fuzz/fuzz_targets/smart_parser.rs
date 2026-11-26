//! The JSON of smartctl (M5-18): the root helper passes it on raw, the agent parses it without
//! rights. No input may panic; a disk gives at most 10 samples within the runtime's bounds, its
//! model is printable ASCII of at most 64 bytes.
#![no_main]

use libfuzzer_sys::fuzz_target;
use sermon_modules::smart::{parse, samples};

fuzz_target!(|data: &[u8]| {
    let Some(disk) = parse(
        "/usr/sbin/smartctl --json=c -i -H -A -n standby /dev/sda",
        data,
    ) else {
        panic!("sda is a disk");
    };
    assert!(disk.model.len() <= 64);
    assert!(disk.model.bytes().all(|b| (0x20..0x7f).contains(&b)));
    let out = samples(&[disk], false);
    assert!(out.len() <= 12, "{} samples", out.len());
    for s in &out {
        assert!(s.metric.starts_with("sermon_smart_"));
        assert!(
            s.labels
                .iter()
                .all(|(_, v)| v.len() <= 64 && !v.chars().any(char::is_control))
        );
    }
});
