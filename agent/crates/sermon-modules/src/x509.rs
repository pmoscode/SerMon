//! The little of X.509 the modules read (M5-08, M5-09): the validity of a certificate in DER.
//! Certificates come from services and files of the host, so the reader is strict and bounded:
//! definite lengths only, at most four length bytes, nothing read beyond its element.

/// A DER element: its tag, its content and what follows it.
fn element(input: &[u8]) -> Option<(u8, &[u8], &[u8])> {
    let (&tag, rest) = input.split_first()?;
    // High tag numbers don't occur in a certificate's structure.
    if tag & 0x1f == 0x1f {
        return None;
    }
    let (&first, rest) = rest.split_first()?;
    let (len, rest) = if first < 0x80 {
        (usize::from(first), rest)
    } else {
        let n = usize::from(first & 0x7f);
        if n == 0 || n > 4 || rest.len() < n {
            return None;
        }
        let len = rest[..n]
            .iter()
            .fold(0usize, |len, b| (len << 8) | usize::from(*b));
        (len, &rest[n..])
    };
    if rest.len() < len {
        return None;
    }
    let (content, rest) = rest.split_at(len);
    Some((tag, content, rest))
}

/// The next element if it has `tag`.
fn expect(input: &[u8], tag: u8) -> Option<(&[u8], &[u8])> {
    let (found, content, rest) = element(input)?;
    (found == tag).then_some((content, rest))
}

const SEQUENCE: u8 = 0x30;
const INTEGER: u8 = 0x02;
const VERSION: u8 = 0xa0;
const UTC_TIME: u8 = 0x17;
const GENERALIZED_TIME: u8 = 0x18;

/// When a certificate is valid, in seconds since the Unix epoch (negative before 1970).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Validity {
    /// Not before.
    pub not_before: i64,
    /// Not after.
    pub not_after: i64,
}

/// The validity of the certificate `der`; `None` if it is no certificate.
#[must_use]
pub fn validity(der: &[u8]) -> Option<Validity> {
    let (certificate, _) = expect(der, SEQUENCE)?;
    let (tbs, _) = expect(certificate, SEQUENCE)?;
    let mut rest = tbs;
    if let Some((VERSION, _, after)) = element(rest) {
        rest = after;
    }
    let (_, rest) = expect(rest, INTEGER)?; // serial number
    let (_, rest) = expect(rest, SEQUENCE)?; // signature algorithm
    let (_, rest) = expect(rest, SEQUENCE)?; // issuer
    let (validity, _) = expect(rest, SEQUENCE)?;
    let (tag, before, rest) = element(validity)?;
    let not_before = time(tag, before)?;
    let (tag, after, _) = element(rest)?;
    let not_after = time(tag, after)?;
    Some(Validity {
        not_before,
        not_after,
    })
}

/// A `UTCTime` (`YYMMDDHHMMSSZ`, years 1950 to 2049) or `GeneralizedTime` (`YYYYMMDDHHMMSSZ`).
fn time(tag: u8, text: &[u8]) -> Option<i64> {
    let digits = text.strip_suffix(b"Z")?;
    if !digits.iter().all(u8::is_ascii_digit) {
        return None;
    }
    let number = |range: std::ops::Range<usize>| {
        digits
            .get(range)?
            .iter()
            .try_fold(0i64, |n, d| Some(n * 10 + i64::from(d - b'0')))
    };
    let (year, rest) = match (tag, digits.len()) {
        (UTC_TIME, 12) => {
            let yy = number(0..2)?;
            (if yy >= 50 { 1900 + yy } else { 2000 + yy }, 2)
        }
        (GENERALIZED_TIME, 14) => (number(0..4)?, 4),
        _ => return None,
    };
    let field = |i: usize| number(rest + 2 * i..rest + 2 * i + 2);
    let (month, day, hour, minute, second) =
        (field(0)?, field(1)?, field(2)?, field(3)?, field(4)?);
    if !(1..=12).contains(&month)
        || !(1..=days_in_month(year, month)).contains(&day)
        || hour > 23
        || minute > 59
        || second > 59
    {
        return None;
    }
    Some(days_from_civil(year, month, day) * 86_400 + hour * 3600 + minute * 60 + second)
}

fn days_in_month(year: i64, month: i64) -> i64 {
    match month {
        2 if year % 4 == 0 && (year % 100 != 0 || year % 400 == 0) => 29,
        2 => 28,
        4 | 6 | 9 | 11 => 30,
        _ => 31,
    }
}

/// Days since 1970-01-01 of a date of the proleptic Gregorian calendar (Howard Hinnant).
fn days_from_civil(year: i64, month: i64, day: i64) -> i64 {
    let y = if month <= 2 { year - 1 } else { year };
    let era = y.div_euclid(400);
    let yoe = y - era * 400;
    let mp = (month + 9) % 12;
    let doy = (153 * mp + 2) / 5 + day - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    era * 146_097 + doe - 719_468
}

#[cfg(test)]
mod tests {
    use super::*;

    fn certificate(not_before: (i32, u8, u8), not_after: (i32, u8, u8)) -> Vec<u8> {
        let mut params = rcgen::CertificateParams::new(vec!["localhost".to_owned()]).unwrap();
        params.not_before = rcgen::date_time_ymd(not_before.0, not_before.1, not_before.2);
        params.not_after = rcgen::date_time_ymd(not_after.0, not_after.1, not_after.2);
        let key = rcgen::KeyPair::generate().unwrap();
        params.self_signed(&key).unwrap().der().to_vec()
    }

    #[test]
    fn reads_the_validity_of_a_certificate() {
        // rcgen writes UTCTime up to 2049 and GeneralizedTime from 2050 (RFC 5280).
        let der = certificate((2026, 1, 1), (2027, 2, 28));
        assert_eq!(
            validity(&der),
            Some(Validity {
                not_before: 1_767_225_600,
                not_after: 1_803_772_800,
            })
        );
        let late = certificate((1969, 12, 31), (2051, 3, 1));
        let v = validity(&late).unwrap();
        assert_eq!(v.not_before, -86_400);
        assert_eq!(v.not_after, 2_561_241_600);
    }

    #[test]
    fn reads_both_time_formats() {
        assert_eq!(time(UTC_TIME, b"700101000000Z"), Some(0));
        assert_eq!(time(UTC_TIME, b"491231235959Z"), Some(2_524_607_999));
        assert_eq!(time(UTC_TIME, b"500101000000Z"), Some(-631_152_000));
        assert_eq!(
            time(GENERALIZED_TIME, b"20240229120000Z"),
            Some(1_709_208_000)
        );
        assert_eq!(
            time(GENERALIZED_TIME, b"99991231235959Z"),
            Some(253_402_300_799)
        );
        for bad in [
            &b"20230229120000Z"[..],
            b"20241301000000Z",
            b"20240100000000Z",
            b"20240101240000Z",
            b"20240101006000Z",
            b"20240101000060Z",
            b"2024010100000Z",
            b"20240101000000",
            b"2024010100000+Z",
        ] {
            assert_eq!(time(GENERALIZED_TIME, bad), None, "{bad:?}");
        }
        assert_eq!(time(UTC_TIME, b"20240101000000Z"), None, "the wrong format");
        assert_eq!(time(0x04, b"700101000000Z"), None);
    }

    #[test]
    fn refuses_what_is_no_certificate() {
        let der = certificate((2026, 1, 1), (2027, 1, 1));
        for cut in [0, 1, 2, 10, 100, der.len() / 2] {
            assert_eq!(validity(&der[..cut]), None, "cut at {cut}");
        }
        assert_eq!(
            validity(b"\x30\x84\xff\xff\xff\xff"),
            None,
            "longer than the input"
        );
        assert_eq!(
            validity(b"\x30\x85\x00\x00\x00\x00\x00"),
            None,
            "five length bytes"
        );
        assert_eq!(validity(b"\x30\x80\x00\x00"), None, "indefinite length");
        assert_eq!(validity(b"\x3f\x00"), None);
        assert_eq!(element(b"\x04\x81\x02ab").map(|e| e.1), Some(&b"ab"[..]));
        assert_eq!(
            element(b"\x04\x84\x00\x00\x00\x02ab").map(|e| e.1),
            Some(&b"ab"[..])
        );
        assert_eq!(
            element(b"\x04\x85\x00\x00\x00\x00\x02ab"),
            None,
            "five length bytes"
        );
    }
}
