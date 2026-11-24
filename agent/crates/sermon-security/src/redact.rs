//! Redaction of secrets in text that leaves the host (KONZEPT §10.3, M3-02): log messages, sudo
//! commands and command lines of the live view.
//!
//! - **Built-in patterns** for what commonly leaks: `password=`/`token: …` and friends (also in
//!   JSON, glued as in `PGPASSWORD=` and as `--any-password value`), `Authorization:
//!   Bearer|Basic`, cookies, passwords in URLs, the password options of `mysql` (`-p…`),
//!   `sshpass -p` and `redis-cli -a`, AWS access keys, private key blocks (and their base64
//!   lines), JSON web tokens, GitHub and Slack tokens and SerMon enrollment tokens (M3-28).
//! - **Rules of the organization** (from the server, M3-03): a regex per rule, compiled by the
//!   `regex` crate (linear time, bounded size). A rule replaces its whole match, or only the group
//!   named `secret` if it has one, with `***`. Empty matches change nothing.
//!
//! Redaction may make a text longer (`***` for one character); whoever bounds the text cuts it
//! afterwards.

use std::borrow::Cow;
use std::sync::LazyLock;

use regex::{Regex, RegexBuilder};

use crate::regex;

/// What a secret is replaced with.
pub const MASK: &str = "***";
/// Rules an organization may have.
pub const MAX_RULES: usize = 32;
/// Longest pattern of a rule.
pub const MAX_PATTERN_BYTES: usize = 1024;
/// Memory a compiled rule may take at most (`RegexBuilder::size_limit`).
const RULE_SIZE_LIMIT: usize = 1024 * 1024;
/// Memory all rules of an organization may take compiled: each gets its share (M3-29), so 32
/// large rules don't add up to 32 MiB.
pub const RULES_BUDGET: usize = 8 * 1024 * 1024;
/// Memory the lazy DFA of a rule may take (per thread that runs it).
const RULE_DFA_LIMIT: usize = 256 * 1024;

/// The compiled size one of `count` rules may take.
#[must_use]
pub fn rule_size_limit(count: usize) -> usize {
    (RULES_BUDGET / count.max(1)).min(RULE_SIZE_LIMIT)
}

/// Key words whose value is a secret: `password=…`, `"token": "…"`, `DB_PASSWORD=…`.
const KEYS: &str = "password|passwd|passphrase|pwd|pass|secret[-_]?key|secret|token|api[-_]?key|access[-_]?key|private[-_]?key";
/// Key words a prefix may be glued to: `PGPASSWORD=…`, `dbpassword: …`.
const GLUED: &str = "[a-z0-9]*(?:password|passwd|passphrase)";
/// A value: quoted (with escaped quotes inside) or up to the next blank.
const VALUE: &str = r#"(?:'[^']*'|"(?:[^"\\]|\\.)*"|\S+)"#;

/// A private key block, up to its end or the end of the text if it was cut.
static PRIVATE_KEY: LazyLock<Regex> = LazyLock::new(|| {
    regex(r"(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|\z)")
});
/// A text that is only base64, as long as a line of a key (PEM: 64, OpenSSH: 70 characters).
static BASE64_LINE: LazyLock<Regex> = LazyLock::new(|| regex(r"(?m)^[A-Za-z0-9+/]{60,}={0,2}$"));
static KEY_VALUE: LazyLock<Regex> = LazyLock::new(|| {
    regex(&format!(
        r#"(?i)(\b(?:[a-z0-9]+[_-])*(?:{GLUED}|{KEYS})\b["']?\s*[=:]\s*){VALUE}"#
    ))
});
/// `--password value`, `--db-password value`, `--client-secret value`.
static FLAG: LazyLock<Regex> =
    LazyLock::new(|| regex(&format!(r"(?i)(--(?:[a-z0-9]+[-_])*(?:{KEYS})\s+){VALUE}")));
/// Password options of common tools: `mysql -pSECRET` (glued; `-p` alone prompts), `sshpass -p
/// SECRET`, `redis-cli -a SECRET`.
static TOOL_FLAG: LazyLock<Regex> = LazyLock::new(|| {
    regex(concat!(
        r"(\b(?i:mysql|mariadb)[a-z]*\b[^\n]*?\s-p)\S+",
        r"|(\bsshpass\b[^\n]*?\s-p\s*)\S+",
        r"|(\bredis-cli\b[^\n]*?\s-a\s+)\S+",
    ))
});
static BEARER: LazyLock<Regex> =
    LazyLock::new(|| regex(r"(?i)(authorization:\s*(?:bearer|basic)\s+)\S+"));
static COOKIE: LazyLock<Regex> = LazyLock::new(|| regex(r"(?i)(\b(?:set-)?cookie:\s*)\S[^\n]*"));
static URL_PASSWORD: LazyLock<Regex> = LazyLock::new(|| regex(r"(://[^:/@\s]+:)[^@\s]+@"));
/// Tokens recognizable without a key in front: AWS access keys, JSON web tokens, GitHub and Slack
/// tokens, SerMon enrollment tokens.
static TOKEN: LazyLock<Regex> = LazyLock::new(|| {
    regex(concat!(
        r"\b(?:AKIA|ASIA)[0-9A-Z]{16}\b",
        r"|\beyJ[A-Za-z0-9_-]{4,}\.eyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]+",
        r"|\b(?:gh[pousr]|github_pat)_[A-Za-z0-9_]{20,}",
        r"|\bxox[abprs]-[A-Za-z0-9-]{10,}",
        r"|\bsme_[A-Za-z0-9_-]{8,}",
    ))
});

/// The built-in patterns in their order (key blocks first: they span lines) with what replaces
/// a match.
fn builtin() -> [(&'static Regex, &'static str); 9] {
    [
        (&*PRIVATE_KEY, MASK),
        (&*BASE64_LINE, MASK),
        (&*KEY_VALUE, "${1}***"),
        (&*FLAG, "${1}***"),
        (&*TOOL_FLAG, "${1}${2}${3}***"),
        (&*BEARER, "${1}***"),
        (&*COOKIE, "${1}***"),
        (&*URL_PASSWORD, "${1}***@"),
        (&*TOKEN, MASK),
    ]
}

/// A rule of the organization as the server sends it.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RuleSpec {
    /// The regex; a group named `secret` limits what is replaced.
    pub pattern: String,
}

/// Why rules cannot be used. The index is the position of the rule.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum RuleError {
    /// More than [`MAX_RULES`].
    #[error("at most {MAX_RULES} rules, got {0}")]
    TooMany(usize),
    /// An empty pattern.
    #[error("rule {0}: the pattern is empty")]
    Empty(usize),
    /// A pattern longer than [`MAX_PATTERN_BYTES`].
    #[error("rule {0}: the pattern is longer than {MAX_PATTERN_BYTES} bytes")]
    TooLong(usize),
    /// The pattern does not compile (syntax, or larger than the size limit).
    #[error("rule {index}: {reason}")]
    Invalid {
        /// Position of the rule.
        index: usize,
        /// What the regex crate says.
        reason: String,
    },
}

/// Replaces secrets: the built-in patterns, then the rules of the organization.
#[derive(Debug, Clone, Default)]
pub struct Redactor {
    rules: Vec<Regex>,
}

impl Redactor {
    /// Only the built-in patterns.
    #[must_use]
    pub fn builtin() -> Self {
        Self::default()
    }

    /// The built-in patterns and `rules`.
    ///
    /// # Errors
    ///
    /// [`RuleError`] for the first rule that cannot be used; then no rule applies.
    pub fn with_rules(rules: &[RuleSpec]) -> Result<Self, RuleError> {
        if rules.len() > MAX_RULES {
            return Err(RuleError::TooMany(rules.len()));
        }
        let size_limit = rule_size_limit(rules.len());
        let compiled = rules
            .iter()
            .enumerate()
            .map(|(index, rule)| {
                if rule.pattern.is_empty() {
                    return Err(RuleError::Empty(index));
                }
                if rule.pattern.len() > MAX_PATTERN_BYTES {
                    return Err(RuleError::TooLong(index));
                }
                RegexBuilder::new(&rule.pattern)
                    .size_limit(size_limit)
                    .dfa_size_limit(RULE_DFA_LIMIT)
                    .build()
                    .map_err(|error| RuleError::Invalid {
                        index,
                        reason: error.to_string(),
                    })
            })
            .collect::<Result<_, _>>()?;
        Ok(Self { rules: compiled })
    }

    /// `text` with every secret replaced by [`MASK`]; borrowed if nothing was found.
    #[must_use]
    pub fn redact<'a>(&self, text: &'a str) -> Cow<'a, str> {
        let mut out = Cow::Borrowed(text);
        for (pattern, replacement) in builtin() {
            if let Cow::Owned(replaced) = pattern.replace_all(&out, replacement) {
                out = Cow::Owned(replaced);
            }
        }
        for rule in &self.rules {
            if let Some(replaced) = apply(rule, &out) {
                out = Cow::Owned(replaced);
            }
        }
        out
    }
}

/// `text` with the matches of `rule` (or their group `secret`) masked; `None` if none was found.
/// Empty matches are skipped: a rule like `x*` would otherwise put a mask between every
/// character.
fn apply(rule: &Regex, text: &str) -> Option<String> {
    let mut out = String::new();
    let mut copied = 0;
    for captures in rule.captures_iter(text) {
        let secret = match captures.name("secret") {
            Some(group) => group,
            None if rule.capture_names().any(|name| name == Some("secret")) => continue,
            None => captures.get(0)?,
        };
        if secret.is_empty() {
            continue;
        }
        out.push_str(&text[copied..secret.start()]);
        out.push_str(MASK);
        copied = secret.end();
    }
    if copied == 0 && out.is_empty() {
        return None;
    }
    out.push_str(&text[copied..]);
    Some(out)
}

/// Replaces secrets with `***` using the built-in patterns.
#[must_use]
pub fn redact(text: &str) -> String {
    Redactor::builtin().redact(text).into_owned()
}

#[cfg(test)]
mod tests {
    use proptest::prelude::*;

    use super::*;

    fn rules(patterns: &[&str]) -> Result<Redactor, RuleError> {
        Redactor::with_rules(
            &patterns
                .iter()
                .map(|p| RuleSpec {
                    pattern: (*p).to_owned(),
                })
                .collect::<Vec<_>>(),
        )
    }

    // betterleaks:allow – every secret below is made up.
    #[test]
    fn redacts_the_built_in_patterns() {
        for (input, want) in [
            ("mysql --password=hunter2 -e x", "mysql --password=*** -e x"),
            ("app PASSWORD='a b c' run", "app PASSWORD=*** run"),
            (
                "curl -H 'Authorization: Bearer eyJabc.def' https://x", // betterleaks:allow
                "curl -H 'Authorization: Bearer *** https://x",
            ),
            (
                "proxy: authorization: basic YWxpY2U6czNjcjN0", // betterleaks:allow
                "proxy: authorization: basic ***",
            ),
            (
                "psql postgres://app:s3cr3t@db/app", // betterleaks:allow
                "psql postgres://app:***@db/app",
            ),
            (
                "tool --token abc123 --verbose",
                "tool --token *** --verbose",
            ),
            ("export AWS=AKIAIOSFODNN7EXAMPLE", "export AWS=***"),
            ("api_key: xyz", "api_key: ***"),
            ("X-Api-Key: 0123abcd", "X-Api-Key: ***"),
            // Names with a prefix, as sudo logs variables of its command line (M2-28).
            (
                "ENV=SERMON_ENROLLMENT_TOKEN=sme_abc123 ; COMMAND=/usr/bin/sermon-agent enroll",
                "ENV=SERMON_ENROLLMENT_TOKEN=*** ; COMMAND=/usr/bin/sermon-agent enroll",
            ),
            ("DB_PASSWORD=s3cret ./run", "DB_PASSWORD=*** ./run"),
            // JSON and quoted keys.
            (
                r#"login {"user":"alice","password":"hunter2"}"#, // betterleaks:allow
                r#"login {"user":"alice","password":***}"#,
            ),
            (
                r#"{"client_secret": "abc def", "x": 1}"#,
                r#"{"client_secret": ***, "x": 1}"#,
            ),
            // Tokens without a key in front.
            (
                "enroll with sme_R2xpdHRlcmluZ1Rva2VuMTIzNDU2Nzg5MA failed",
                "enroll with *** failed",
            ),
            (
                "jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJhbGljZSJ9.c2lnbmF0dXJlLXZhbHVl ok", // betterleaks:allow
                "jwt *** ok",
            ),
            (
                "clone with ghp_0123456789abcdefghijABCDEFGHIJ012345", // betterleaks:allow
                "clone with ***",
            ),
            (
                "hook xoxb-1234567890-abcdefghij failed", // betterleaks:allow
                "hook *** failed",
            ),
            // Nothing secret.
            ("/usr/bin/apt-get upgrade", "/usr/bin/apt-get upgrade"),
            ("passwd alice", "passwd alice"),
            (
                "Accepted publickey for alice from 192.0.2.7 port 22 ssh2",
                "Accepted publickey for alice from 192.0.2.7 port 22 ssh2",
            ),
            ("token expired for alice", "token expired for alice"),
            (
                "https://example.org:8443/path",
                "https://example.org:8443/path",
            ),
        ] {
            assert_eq!(redact(input), want, "{input}");
        }
    }

    /// The armor line of a key; built at run time, so secret scanners don't take the tests for
    /// leaked keys.
    fn armor(edge: &str, kind: &str) -> String {
        format!("-----{edge} {kind}{}-----", ["PRIVATE", "KEY"].join(" "))
    }

    #[test]
    fn private_keys_are_redacted_as_a_block_and_line_by_line() {
        let block = format!(
            "key: {}\nb3BlbnNzaC1rZXktdjEAAAAABG5vbmU=\n{} done",
            armor("BEGIN", "OPENSSH "),
            armor("END", "OPENSSH ")
        );
        assert_eq!(redact(&block), "key: *** done");
        // Cut before the end: everything after the start goes.
        let cut = format!(
            "{}\nMIIEowIBAAKCAQEA0Z3VS5JJcds3xfn",
            armor("BEGIN", "RSA ")
        );
        assert_eq!(redact(&cut), "***");
        // The lines of a key logged one by one (journald: one entry per line).
        assert_eq!(redact(&armor("BEGIN", "")), "***");
        assert_eq!(
            redact("MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7VJTUt9Us8cKj"),
            "***"
        );
        assert_eq!(
            redact("b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW"),
            "***"
        );
        // Short base64 and public material stays.
        assert_eq!(redact("aGVsbG8gd29ybGQ="), "aGVsbG8gd29ybGQ=");
        assert_eq!(
            redact("-----BEGIN CERTIFICATE-----"),
            "-----BEGIN CERTIFICATE-----"
        );
    }

    #[test]
    fn borrows_when_nothing_is_found() {
        let redactor = Redactor::builtin();
        assert!(matches!(
            redactor.redact("nginx: worker process started"),
            Cow::Borrowed(_)
        ));
        assert!(matches!(redactor.redact("password=x"), Cow::Owned(_)));
    }

    #[test]
    fn rules_of_the_organization_replace_the_match_or_the_secret_group() {
        let redactor = rules(&[r"customer-\d{6}", r"(?i)pin\s*(?P<secret>\d{4})\b"]).unwrap();
        assert_eq!(
            redactor.redact("order for customer-123456 shipped"),
            "order for *** shipped"
        );
        assert_eq!(redactor.redact("card PIN 1234 ok"), "card PIN *** ok");
        // The built-in patterns still apply, and before the rules.
        assert_eq!(
            redactor.redact("password=hunter2 customer-654321"),
            "password=*** ***"
        );
        // A rule that matches the empty string changes nothing there.
        let empty = rules(&[r"x*"]).unwrap();
        assert_eq!(empty.redact("abc"), "abc");
        assert_eq!(empty.redact("axxb"), "a***b");
        // An optional secret group that did not take part.
        let optional = rules(&[r"id=(?P<secret>\d+)?"]).unwrap();
        assert_eq!(optional.redact("id= id=42"), "id= id=***");
    }

    #[test]
    fn rejects_rules_that_cannot_be_used() {
        assert_eq!(rules(&[]).map(|_| ()), Ok(()));
        let many = vec!["a"; MAX_RULES + 1];
        assert_eq!(rules(&many).unwrap_err(), RuleError::TooMany(MAX_RULES + 1));
        assert!(rules(&vec!["a"; MAX_RULES]).is_ok());
        assert_eq!(rules(&["a", ""]).unwrap_err(), RuleError::Empty(1));
        let long = "a".repeat(MAX_PATTERN_BYTES + 1);
        assert_eq!(rules(&[&long]).unwrap_err(), RuleError::TooLong(0));
        assert!(matches!(
            rules(&["ok", "(unclosed"]).unwrap_err(),
            RuleError::Invalid { index: 1, .. }
        ));
        // Backreferences and look-around do not exist in a linear-time engine.
        for pattern in [r"(a)\1", r"(?=x)", r"(?<!x)y"] {
            assert!(
                matches!(rules(&[pattern]).unwrap_err(), RuleError::Invalid { .. }),
                "{pattern}"
            );
        }
        // Too large once compiled, though short to write: 100 Unicode word characters take more
        // than the 1 MiB of a rule (the regex crate alone would allow 10 MiB).
        assert!(matches!(
            rules(&[r"\w{100}"]).unwrap_err(),
            RuleError::Invalid { index: 0, .. }
        ));
        assert!(rules(&[r"\w{20}"]).is_ok());
    }

    // M3-29: all rules together have a budget; each gets its share of it.
    #[test]
    fn the_rules_share_a_budget() {
        assert_eq!(rule_size_limit(0), 1024 * 1024);
        assert_eq!(rule_size_limit(8), 1024 * 1024);
        assert_eq!(rule_size_limit(MAX_RULES), RULES_BUDGET / MAX_RULES);
        // Ten Unicode word characters fit alone, not as 32 rules.
        assert!(rules(&[r"\w{10}"]).is_ok());
        let heavy = vec![r"\w{10}"; MAX_RULES];
        assert!(matches!(
            rules(&heavy).unwrap_err(),
            RuleError::Invalid { index: 0, .. }
        ));
        // ASCII classes stay small.
        assert!(rules(&vec!["[A-Za-z0-9_]{20}"; MAX_RULES]).is_ok());
    }

    // A rough cost per line; run with `cargo test --release -p sermon-security -- --ignored
    // --nocapture measure` (numbers in IMPLEMENTIERUNGSPLAN M3-02).
    #[test]
    #[ignore = "measurement"]
    fn measure() {
        let redactor = rules(&[r"customer-\d{6}", r"(?i)pin\s*(?P<secret>\d{4})\b"]).unwrap();
        let lines = [
            "Nov 21 19:33:14 web-01 nginx[812]: 192.0.2.7 - - \"GET /index.html HTTP/1.1\" 200 512",
            "Started Session 42 of User alice.",
            "app: login {\"user\":\"alice\",\"password\":\"hunter2\"} from 192.0.2.7",
            "Failed password for invalid user admin from 192.0.2.66 port 52462 ssh2",
        ];
        let rounds = 100_000;
        let start = std::time::Instant::now();
        for _ in 0..rounds {
            for line in lines {
                std::hint::black_box(redactor.redact(std::hint::black_box(line)));
            }
        }
        let per_line = start.elapsed() / (rounds * 4);
        println!("redaction: {per_line:?} per line");
    }

    proptest! {
        #[test]
        fn an_embedded_secret_never_survives(
            before in "[a-z ]{0,40}",
            key in prop::sample::select(vec![
                "password", "PASSWORD", "db_password", "passwd", "pwd", "secret", "client_secret",
                "token", "auth_token", "api_key", "apikey", "access-key", "private_key",
            ]),
            separator in prop::sample::select(vec!["=", ": ", " = ", ":", "\":\"", "' = '"]),
            secret in "[A-Z0-9]{8,24}",
            after in "[a-z ]{0,40}",
        ) {
            let text = format!("{before} {key}{separator}{secret} {after}");
            let out = redact(&text);
            prop_assert!(!out.contains(&secret), "{text:?} → {out:?}");
        }

        #[test]
        fn flags_bearer_and_urls_never_keep_the_secret(
            secret in "[A-Za-z0-9]{8,24}",
            form in 0usize..4,
        ) {
            let text = match form {
                0 => format!("run --password {secret} now"),
                1 => format!("Authorization: Bearer {secret}"),
                2 => format!("mysql://alice:{secret}@db:3306/x"),
                _ => format!("{}\n{secret}\n{}", armor("BEGIN", "EC "), armor("END", "EC ")),
            };
            prop_assert!(!redact(&text).contains(&secret), "{text:?}");
        }

        #[test]
        fn never_panics_and_keeps_text_without_secrets(text in "[a-z .,/()\\[\\]-]{0,200}") {
            // Lowercase prose without key words, separators or tokens stays as it is.
            let out = Redactor::builtin().redact(&text);
            prop_assume!(!["password", "passwd", "pwd", "pass", "secret", "token", "key", "basic", "bearer"]
                .iter().any(|w| text.contains(w)));
            prop_assert_eq!(out.as_ref(), text.as_str());
        }

        #[test]
        fn rules_never_panic(pattern in "\\PC{0,40}", text in "\\PC{0,200}") {
            if let Ok(redactor) = rules(&[pattern.as_str()]) {
                let _ = redactor.redact(&text);
            }
        }
    }
}
