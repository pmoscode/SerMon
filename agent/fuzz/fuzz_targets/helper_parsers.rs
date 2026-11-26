//! What the root helper reads before it trusts anything (M4-31): the minisign signature of a
//! release – text of the server, checked as root – and the security list of apt, whose names
//! become arguments of apt-get. No input may panic; a signature without the key's signature never
//! verifies; a name taken is a Debian package name; no two versions are newer than each other.
#![no_main]

use libfuzzer_sys::fuzz_target;
use sermon_helper::host::security_packages;
use sermon_helper::minisign::{self, PublicKey};

// A key of the release format: base64 of "Ed", an 8-byte ID and 32 bytes.
const KEY: &str = "RWQBAgMEBQYHCAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA";

fuzz_target!(|data: &[u8]| {
    let text = String::from_utf8_lossy(data);
    let (signature, rest) = text.split_at(text.floor_char_boundary(text.len() / 2));
    let _ = PublicKey::parse(signature);
    let key = PublicKey::parse(KEY).expect("the test key");
    // Nobody holds the private key of an all-zero public key: nothing verifies.
    assert!(minisign::verify(&[key], signature, rest.as_bytes()).is_err());
    for name in security_packages(&text) {
        assert!(name.len() >= 2 && name.as_bytes()[0].is_ascii_alphanumeric());
        assert!(
            name.bytes()
                .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b"+-.".contains(&b))
        );
    }
    let (a, b) = rest.split_at(rest.floor_char_boundary(rest.len() / 2));
    assert!(!(minisign::newer(a, b) && minisign::newer(b, a)));
    if minisign::valid_version(a) {
        assert!(!minisign::newer(a, a));
    }
});
