//! minisign signatures of the release key (ADR 0012, ADR 0036): algorithm "Ed" (Ed25519 over
//! the file, not pre-hashed), key ID, and the global signature over the trusted comment, which
//! names the file and the version. The same checks as `install.sh`.

use std::cmp::Ordering;

use ring::signature::{ED25519, UnparsedPublicKey};

/// A release public key: its ID and the Ed25519 key.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PublicKey {
    id: [u8; 8],
    key: [u8; 32],
}

/// The release keys compiled into the helper (`SERMON_RELEASE_KEY`, for a rotation also
/// `SERMON_RELEASE_KEY_NEXT`): without one there is no self-update.
#[must_use]
pub fn release_keys() -> Vec<PublicKey> {
    [
        option_env!("SERMON_RELEASE_KEY"),
        option_env!("SERMON_RELEASE_KEY_NEXT"),
    ]
    .into_iter()
    .flatten()
    .filter_map(|k| PublicKey::parse(k.trim()))
    .collect()
}

impl PublicKey {
    /// A key as `release-sign.sh pubkey` prints it (base64 of "Ed", ID and key).
    #[must_use]
    pub fn parse(text: &str) -> Option<Self> {
        let raw = base64(text)?;
        if raw.len() != 42 || &raw[..2] != b"Ed" {
            return None;
        }
        Some(Self {
            id: raw[2..10].try_into().ok()?,
            key: raw[10..].try_into().ok()?,
        })
    }
}

/// A checked signature: what its trusted comment says.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Signed {
    pub file: String,
    pub version: String,
}

/// Checks `signature` (the text of a `.minisig`) over `data` against `keys` and returns the file
/// and version of its trusted comment.
///
/// # Errors
///
/// Why the signature is no valid one of these keys.
pub fn verify(keys: &[PublicKey], signature: &str, data: &[u8]) -> Result<Signed, String> {
    let lines: Vec<&str> = signature.lines().collect();
    let [_, sig, trusted, global, ..] = lines[..] else {
        return Err("malformed signature file".into());
    };
    let sig = base64(sig)
        .filter(|s| s.len() == 74)
        .ok_or("malformed signature")?;
    if &sig[..2] != b"Ed" {
        return Err("unsupported signature algorithm (expected minisign Ed)".into());
    }
    let key = keys
        .iter()
        .find(|k| k.id == sig[2..10])
        .ok_or("signed with another key than the release key")?;
    let trusted = trusted
        .strip_prefix("trusted comment: ")
        .ok_or("malformed trusted comment")?;
    let global = base64(global)
        .filter(|g| g.len() == 64)
        .ok_or("malformed signature")?;
    let ed = UnparsedPublicKey::new(&ED25519, &key.key);
    ed.verify(data, &sig[10..])
        .map_err(|_| "the signature does not match the file")?;
    ed.verify(&[&sig[10..], trusted.as_bytes()].concat(), &global)
        .map_err(|_| "the signature does not match its trusted comment")?;
    let field = |name: &str| {
        trusted
            .split('\t')
            .find_map(|f| f.strip_prefix(name))
            .map(str::to_owned)
            .ok_or_else(|| format!("the trusted comment names no {name}"))
    };
    Ok(Signed {
        file: field("file:")?,
        version: field("version:")?,
    })
}

/// Standard base64 with padding; `None` for anything else.
fn base64(text: &str) -> Option<Vec<u8>> {
    let value = |c: u8| match c {
        b'A'..=b'Z' => Some(c - b'A'),
        b'a'..=b'z' => Some(c - b'a' + 26),
        b'0'..=b'9' => Some(c - b'0' + 52),
        b'+' => Some(62),
        b'/' => Some(63),
        _ => None,
    };
    let bytes = text.trim().as_bytes();
    if bytes.is_empty() || !bytes.len().is_multiple_of(4) {
        return None;
    }
    let mut out = Vec::with_capacity(bytes.len() / 4 * 3);
    for (i, quad) in bytes.chunks(4).enumerate() {
        let last = i == bytes.len() / 4 - 1;
        let pad = quad.iter().rev().take_while(|&&c| c == b'=').count();
        if pad > 2 || (pad > 0 && !last) {
            return None;
        }
        let mut n = 0u32;
        for &c in &quad[..4 - pad] {
            n = n << 6 | u32::from(value(c)?);
        }
        n <<= 6 * pad;
        out.extend_from_slice(&n.to_be_bytes()[1..4 - pad]);
    }
    Some(out)
}

/// A semantic version (MAJOR.MINOR.PATCH with an optional pre-release), at most 64 bytes.
#[must_use]
pub fn valid_version(v: &str) -> bool {
    parse(v).is_some()
}

fn parse(v: &str) -> Option<([u64; 3], Option<&str>)> {
    if v.len() > 64 {
        return None;
    }
    let (core, pre) = match v.split_once('-') {
        Some((c, p)) => (c, Some(p)),
        None => (v, None),
    };
    let numbers: Vec<u64> = core
        .split('.')
        .map(|n| {
            (!n.is_empty() && n.bytes().all(|b| b.is_ascii_digit()))
                .then(|| n.parse().ok())
                .flatten()
        })
        .collect::<Option<_>>()?;
    let ok_pre = pre.is_none_or(|p| {
        p.split('.')
            .all(|i| !i.is_empty() && i.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'-'))
    });
    (numbers.len() == 3 && ok_pre).then(|| ([numbers[0], numbers[1], numbers[2]], pre))
}

/// Whether `left` is a newer version than `right` (`SemVer` precedence); false if either is
/// invalid.
#[must_use]
pub fn newer(left: &str, right: &str) -> bool {
    let (Some((core_l, pre_l)), Some((core_r, pre_r))) = (parse(left), parse(right)) else {
        return false;
    };
    core_l.cmp(&core_r).then_with(|| pre_release(pre_l, pre_r)) == Ordering::Greater
}

/// `SemVer` precedence of pre-releases: none is the release itself and comes last.
fn pre_release(left: Option<&str>, right: Option<&str>) -> Ordering {
    let (left, right) = match (left, right) {
        (None, None) => return Ordering::Equal,
        (None, Some(_)) => return Ordering::Greater,
        (Some(_), None) => return Ordering::Less,
        (Some(l), Some(r)) => (l, r),
    };
    let (mut left, mut right) = (left.split('.'), right.split('.'));
    loop {
        let (ident_l, ident_r) = match (left.next(), right.next()) {
            (Some(l), Some(r)) => (l, r),
            (l, r) => return l.is_some().cmp(&r.is_some()),
        };
        let order = match (ident_l.parse::<u64>(), ident_r.parse::<u64>()) {
            (Ok(num_l), Ok(num_r)) => num_l.cmp(&num_r),
            (Ok(_), Err(_)) => Ordering::Less,
            (Err(_), Ok(_)) => Ordering::Greater,
            (Err(_), Err(_)) => ident_l.cmp(ident_r),
        };
        if order != Ordering::Equal {
            return order;
        }
    }
}

#[cfg(test)]
pub(crate) mod tests;
