use ring::rand::SystemRandom;
use ring::signature::{Ed25519KeyPair, KeyPair};

use super::*;

/// A release key of a test: signs like `scripts/release-sign.sh`.
pub(crate) struct Signer {
    pair: Ed25519KeyPair,
    pub(crate) key: PublicKey,
}

impl std::fmt::Debug for Signer {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Signer")
            .field("key", &self.key)
            .finish_non_exhaustive()
    }
}

fn encode(bytes: &[u8]) -> String {
    const ALPHABET: &[u8] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut out = String::new();
    for chunk in bytes.chunks(3) {
        let n = u32::from(chunk[0]) << 16
            | u32::from(*chunk.get(1).unwrap_or(&0)) << 8
            | u32::from(*chunk.get(2).unwrap_or(&0));
        for i in 0..4 {
            if i <= chunk.len() {
                out.push(char::from(
                    ALPHABET[usize::try_from(n >> (18 - 6 * i) & 63).unwrap()],
                ));
            } else {
                out.push('=');
            }
        }
    }
    out
}

impl Signer {
    pub(crate) fn new(id: u8) -> Self {
        let pkcs8 = Ed25519KeyPair::generate_pkcs8(&SystemRandom::new()).unwrap();
        let pair = Ed25519KeyPair::from_pkcs8(pkcs8.as_ref()).unwrap();
        let line = encode(&[b"Ed".as_slice(), &[id; 8], pair.public_key().as_ref()].concat());
        let key = PublicKey::parse(&line).unwrap();
        Self { pair, key }
    }

    /// The `.minisig` of `data` with the trusted comment `trusted`.
    pub(crate) fn sign_with(&self, data: &[u8], trusted: &str) -> String {
        let sig = self.pair.sign(data);
        let global = self.pair.sign(&[sig.as_ref(), trusted.as_bytes()].concat());
        format!(
            "untrusted comment: test\n{}\ntrusted comment: {trusted}\n{}\n",
            encode(&[b"Ed".as_slice(), &self.key.id, sig.as_ref()].concat()),
            encode(global.as_ref())
        )
    }

    pub(crate) fn sign(&self, data: &[u8], file: &str, version: &str) -> String {
        self.sign_with(
            data,
            &format!("timestamp:1\tfile:{file}\thashed:0\tversion:{version}"),
        )
    }
}

#[test]
fn verifies_the_file_and_its_trusted_comment() {
    let signer = Signer::new(7);
    let other = Signer::new(8);
    let data = b"\x7fELF agent";
    let sig = signer.sign(data, "sermon-agent-linux-arm64", "1.2.3");
    let keys = [other.key.clone(), signer.key.clone()];
    assert_eq!(
        verify(&keys, &sig, data),
        Ok(Signed {
            file: "sermon-agent-linux-arm64".into(),
            version: "1.2.3".into()
        })
    );
    // Another file, another key, a changed comment, a broken file.
    assert!(verify(&keys, &sig, b"\x7fELF agenT").is_err());
    assert!(
        verify(std::slice::from_ref(&other.key), &sig, data)
            .unwrap_err()
            .contains("another key")
    );
    let forged = sig.replace("version:1.2.3", "version:9.9.9");
    assert!(
        verify(&keys, &forged, data)
            .unwrap_err()
            .contains("trusted comment")
    );
    for broken in [
        "",
        "x\ny\nz\nw\n",
        &sig.replace("trusted comment: ", "comment: "),
    ] {
        assert!(verify(&keys, broken, data).is_err(), "{broken:?}");
    }
    // Prehashed ("ED") is not what release-sign.sh writes.
    let lines: Vec<&str> = sig.lines().collect();
    let mut raw = base64(lines[1]).unwrap();
    raw[1] = b'D';
    let ed = sig.replace(lines[1], &encode(&raw));
    assert!(verify(&keys, &ed, data).unwrap_err().contains("algorithm"));
    let no_version = signer.sign_with(data, "timestamp:1\tfile:sermon-agent-linux-arm64");
    assert!(
        verify(&keys, &no_version, data)
            .unwrap_err()
            .contains("version")
    );
}

#[test]
fn decodes_base64_strictly() {
    assert_eq!(base64("aGk=").unwrap(), b"hi");
    assert_eq!(base64("aGVsbG8h").unwrap(), b"hello!");
    for bad in ["", "aGk", "a=Gk", "aG==aGk=", "aGk*", "a==="] {
        assert_eq!(base64(bad), None, "{bad:?}");
    }
    assert_eq!(PublicKey::parse("aGk="), None);
    assert!(release_keys().len() <= 2);
}

#[test]
fn orders_versions_by_semver() {
    for (a, b) in [
        ("1.0.1", "1.0.0"),
        ("1.10.0", "1.9.9"),
        ("2.0.0", "1.99.99"),
        ("1.0.0", "1.0.0-rc.1"),
        ("1.0.0-rc.2", "1.0.0-rc.1"),
        ("1.0.0-rc.10", "1.0.0-rc.9"),
        ("1.0.0-rc.1.1", "1.0.0-rc.1"),
        ("1.0.0-beta", "1.0.0-alpha"),
        ("1.0.0-alpha", "1.0.0-1"),
        ("0.0.3-e2e", "0.0.1-e2e"),
    ] {
        assert!(newer(a, b), "{a} > {b}");
        assert!(!newer(b, a), "not {b} > {a}");
    }
    assert!(!newer("1.0.0", "1.0.0"));
    for bad in [
        "e2e",
        "1.0",
        "1.0.0.0",
        "01.a.0",
        "1.0.0-",
        "1.0.0-a..b",
        "1.0.0-a_b",
        "-1.0.0",
    ] {
        assert!(!valid_version(bad), "{bad}");
        assert!(!newer(bad, "0.0.1") && !newer("9.9.9", bad), "{bad}");
    }
    assert!(!valid_version(&format!("1.0.0-{}", "a".repeat(60))));
    assert!(valid_version("0.5.0") && valid_version("1.2.3-rc.1"));
}
