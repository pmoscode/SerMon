//! The parsers of sermon-updates read what apt, dnf and apk print from third-party package
//! sources (M4-06): no input may panic, and every field taken is 1 to 256 printable ASCII
//! characters – the text the agent reports and the UI shows.
#![no_main]

use libfuzzer_sys::fuzz_target;
use sermon_updates::{MAX_FIELD, Package, Reboot};

fn plain(s: &str, may_be_empty: bool) -> bool {
    (may_be_empty || !s.is_empty())
        && s.len() <= MAX_FIELD
        && s.bytes().all(|b| b.is_ascii_graphic())
}

fn check(packages: &[Package]) {
    for p in packages {
        assert!(plain(&p.name, false) && plain(&p.candidate, false));
        assert!(plain(&p.installed, true) && plain(&p.source, true));
    }
}

fuzz_target!(|data: &[u8]| {
    let text = String::from_utf8_lossy(data);
    // The second half doubles as dnf's security list.
    let (a, b) = text.split_at(text.floor_char_boundary(text.len() / 2));
    check(&sermon_updates::apt(&text));
    check(&sermon_updates::dnf(a, b));
    check(&sermon_updates::apk(&text));
    let (kept, total, security) = sermon_updates::bounded(sermon_updates::apt(&text), 3);
    assert!(kept.len() <= 3 && security <= total);
    for reboot in [
        sermon_updates::dnf_reboot(&text),
        sermon_updates::apt_reboot(true, &text),
    ] {
        if let Reboot::Required(reason) = reboot {
            assert!(!reason.contains('\n'));
        }
    }
    let modules: Vec<String> = b.lines().map(str::to_owned).collect();
    let _ = sermon_updates::apk_reboot(a, &modules);
});
