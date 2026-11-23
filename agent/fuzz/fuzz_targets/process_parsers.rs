//! The parsers of /proc/<pid>/{stat,status,cmdline,cgroup} and /etc/passwd must handle any input
//! (M3-13): process names and command lines are chosen by whoever starts a process.
#![no_main]

use libfuzzer_sys::fuzz_target;
use sermon_collectors::process::{
    parse_cgroup, parse_cmdline, parse_passwd, parse_stat, parse_status,
};

fuzz_target!(|data: &[u8]| {
    let text = String::from_utf8_lossy(data);
    if let Ok(stat) = parse_stat(&text) {
        // The name is what is between the first ( and the last ).
        assert!(text.contains(&format!("({})", stat.comm)));
    }
    let _ = parse_status(&text);
    let place = parse_cgroup(&text);
    if let Some(id) = place.container {
        assert!(
            id.len() == 12
                && id
                    .bytes()
                    .all(|b| b.is_ascii_hexdigit() && !b.is_ascii_uppercase())
        );
    }
    let _ = parse_passwd(&text);
    let line = parse_cmdline(data);
    assert!(!line.contains('\0'));
});
