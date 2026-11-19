//! SHA-256 fingerprints of certificates, for pinning the server certificate at enrollment.

use std::fmt;
use std::str::FromStr;

/// SHA-256 over the DER encoding of a certificate.
#[derive(Clone, Copy, PartialEq, Eq, Hash)]
pub struct Fingerprint([u8; 32]);

/// The text is not a SHA-256 fingerprint.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
#[error("not a SHA-256 fingerprint (expected sha256:AB:CD:… with 32 bytes in hex)")]
pub struct InvalidFingerprint;

impl Fingerprint {
    /// The fingerprint of a DER-encoded certificate.
    #[must_use]
    pub fn of(der: &[u8]) -> Self {
        let digest = ring::digest::digest(&ring::digest::SHA256, der);
        let mut bytes = [0; 32];
        bytes.copy_from_slice(digest.as_ref());
        Self(bytes)
    }
}

impl FromStr for Fingerprint {
    type Err = InvalidFingerprint;

    /// Accepts `sha256:AB:CD:…` as shown by `openssl x509 -fingerprint -sha256` and by the UI:
    /// the `sha256:` prefix is optional, colons are optional, case doesn't matter.
    fn from_str(text: &str) -> Result<Self, Self::Err> {
        let text = text.trim();
        let hex = match text.get(..7) {
            Some(prefix) if prefix.eq_ignore_ascii_case("sha256:") => &text[7..],
            _ => text,
        };
        let digits: Vec<u8> = hex.bytes().filter(|byte| *byte != b':').collect();
        if digits.len() != 64 {
            return Err(InvalidFingerprint);
        }
        let mut bytes = [0; 32];
        let (pairs, _) = digits.as_chunks::<2>();
        for (byte, pair) in bytes.iter_mut().zip(pairs) {
            let pair = std::str::from_utf8(pair).map_err(|_| InvalidFingerprint)?;
            *byte = u8::from_str_radix(pair, 16).map_err(|_| InvalidFingerprint)?;
        }
        Ok(Self(bytes))
    }
}

impl fmt::Display for Fingerprint {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("sha256:")?;
        for (index, byte) in self.0.iter().enumerate() {
            if index > 0 {
                f.write_str(":")?;
            }
            write!(f, "{byte:02X}")?;
        }
        Ok(())
    }
}

impl fmt::Debug for Fingerprint {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "Fingerprint({self})")
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    // SHA-256 of the empty input.
    const EMPTY: &str = "sha256:E3:B0:C4:42:98:FC:1C:14:9A:FB:F4:C8:99:6F:B9:24:27:AE:41:E4:64:9B:93:4C:A4:95:99:1B:78:52:B8:55";

    #[test]
    fn fingerprint_of_der_is_its_sha256() {
        assert_eq!(Fingerprint::of(b"").to_string(), EMPTY);
    }

    #[test]
    fn parses_all_common_notations() {
        let expected = Fingerprint::of(b"");
        let plain = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855";
        for text in [
            EMPTY,
            &EMPTY.to_lowercase(),
            &EMPTY["sha256:".len()..],
            &EMPTY.replace("sha256:", "SHA256:"),
            plain,
            &format!("  {plain}\n"),
        ] {
            assert_eq!(text.parse::<Fingerprint>(), Ok(expected), "{text}");
        }
    }

    #[test]
    fn rejects_malformed_fingerprints() {
        for text in [
            "",
            "sha256:",
            "sha1:AB:CD",
            "sha256:E3:B0",
            &format!("{EMPTY}:00"),
            &EMPTY.replace("E3", "G3"),
            &EMPTY.replace("E3", "é"),
        ] {
            assert_eq!(
                text.parse::<Fingerprint>(),
                Err(InvalidFingerprint),
                "{text}"
            );
        }
    }
}
