//! Redaction of secrets in text that leaves the host (KONZEPT §10.3). For now only the built-in
//! patterns, used for sudo commands; the configurable rules per organization come with the log
//! pipeline (M3).

use std::borrow::Cow;
use std::sync::LazyLock;

use regex::Regex;

use crate::regex;

static KEY_VALUE: LazyLock<Regex> = LazyLock::new(|| {
    regex(
        r"(?i)(\b(?:password|passwd|pwd|pass|secret|token|api[-_]?key|access[-_]?key|private[-_]?key)\b\s*[=:]\s*)(?:'[^']*'|\x22[^\x22]*\x22|\S+)",
    )
});
static FLAG: LazyLock<Regex> = LazyLock::new(|| {
    regex(
        r"(?i)(--(?:password|passwd|pass|secret|token|api[-_]?key)\s+)(?:'[^']*'|\x22[^\x22]*\x22|\S+)",
    )
});
static BEARER: LazyLock<Regex> =
    LazyLock::new(|| regex(r"(?i)(authorization:\s*(?:bearer|basic)\s+)\S+"));
static URL_PASSWORD: LazyLock<Regex> = LazyLock::new(|| regex(r"(://[^:/@\s]+:)[^@\s]+@"));
static AWS_KEY: LazyLock<Regex> = LazyLock::new(|| regex(r"\b(?:AKIA|ASIA)[0-9A-Z]{16}\b"));

/// Replaces secrets with `***`.
#[must_use]
pub fn redact(text: &str) -> String {
    let mut out = Cow::Borrowed(text);
    for (pattern, replacement) in [
        (&*KEY_VALUE, "${1}***"),
        (&*FLAG, "${1}***"),
        (&*BEARER, "${1}***"),
        (&*URL_PASSWORD, "${1}***@"),
        (&*AWS_KEY, "***"),
    ] {
        if let Cow::Owned(replaced) = pattern.replace_all(&out, replacement) {
            out = Cow::Owned(replaced);
        }
    }
    out.into_owned()
}

#[cfg(test)]
mod tests {
    use super::*;

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
                "psql postgres://app:s3cr3t@db/app", // betterleaks:allow
                "psql postgres://app:***@db/app",
            ),
            (
                "tool --token abc123 --verbose",
                "tool --token *** --verbose",
            ),
            ("export AWS=AKIAIOSFODNN7EXAMPLE", "export AWS=***"),
            ("api_key: xyz", "api_key: ***"),
            ("/usr/bin/apt-get upgrade", "/usr/bin/apt-get upgrade"),
            ("passwd alice", "passwd alice"),
        ] {
            assert_eq!(redact(input), want, "{input}");
        }
    }
}
