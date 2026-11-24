//! Redaction of command lines with their arguments (M3-29): `/proc/<pid>/cmdline` separates the
//! arguments with NUL. Joined with spaces first, `--password 'a b'` would keep its second word
//! and `mysql -pSECRET` would need its program in a regex. So the argument after a password
//! option is masked as a whole and glued passwords of known tools too; then the line, joined with
//! spaces, goes through the usual redaction (built-in patterns and the organization's rules).

use std::sync::LazyLock;

use regex::Regex;

use crate::redact::MASK;

/// An option whose next argument is a secret: `--password`, `--db-password`, `--client-secret`,
/// `--access-token`, `-storepass` (keytool), `--api-key`.
static SECRET_OPTION: LazyLock<Regex> = LazyLock::new(|| {
    crate::regex(
        r"(?i)^--?(?:[a-z0-9]+[-_])*(?:[a-z0-9]*pass(?:word|wd|phrase)?|secret|token|api[-_]?key|(?:secret|access|private)[-_]?key)$",
    )
});

/// The program's name without its directory.
fn program(args: &[&str]) -> &'static str {
    let name = args
        .first()
        .and_then(|a| a.rsplit('/').next())
        .unwrap_or_default();
    if name.starts_with("mysql") || name.starts_with("mariadb") {
        "mysql"
    } else if name == "sshpass" {
        "sshpass"
    } else if name == "redis-cli" {
        "redis-cli"
    } else {
        ""
    }
}

/// The command line `raw` (arguments separated by NUL) with secrets masked, its arguments
/// separated by spaces. `redact` redacts the joined line (built-in patterns and the organization's
/// rules).
#[must_use]
pub fn redact_args(raw: &str, redact: impl Fn(&str) -> String) -> String {
    let args: Vec<&str> = raw.split('\0').collect();
    let tool = program(&args);
    let mut out = Vec::with_capacity(args.len());
    let mut next_is_secret = false;
    for arg in &args {
        if next_is_secret {
            out.push(MASK.to_owned());
            next_is_secret = false;
            continue;
        }
        let glued = match tool {
            // mysql -pSECRET (a lone -p prompts), sshpass -pSECRET
            "mysql" | "sshpass" => arg.len() > 2 && arg.starts_with("-p") && !arg.starts_with("--"),
            _ => false,
        };
        if glued {
            out.push(format!("-p{MASK}"));
            continue;
        }
        next_is_secret = SECRET_OPTION.is_match(arg)
            || (tool == "sshpass" && *arg == "-p")
            || (tool == "redis-cli" && *arg == "-a");
        out.push((*arg).to_owned());
    }
    redact(&out.join(" "))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::Redactor;

    fn redacted(raw: &str) -> String {
        let redactor = Redactor::builtin();
        redact_args(raw, |a| redactor.redact(a).into_owned())
    }

    #[test]
    fn masks_the_argument_after_a_password_option() {
        for (raw, want) in [
            // betterleaks:allow
            ("mysql\0-uroot\0-pS3cret\0db", "mysql -uroot -p*** db"),
            ("/usr/bin/mysqldump\0-pS3cret", "/usr/bin/mysqldump -p***"),
            (
                "app\0--password\0a b c\0--verbose",
                "app --password *** --verbose",
            ),
            (
                "app\0--db-password\0x\0--client-secret\0y",
                "app --db-password *** --client-secret ***",
            ),
            ("sshpass\0-p\0hunter2\0ssh\0host", "sshpass -p *** ssh host"),
            ("sshpass\0-phunter2\0ssh", "sshpass -p*** ssh"),
            (
                "redis-cli\0-h\0db\0-a\0pw\0ping",
                "redis-cli -h db -a *** ping",
            ),
            (
                "keytool\0-list\0-storepass\0changeit",
                "keytool -list -storepass ***",
            ),
            // Passwords with spaces: one argument, masked as a whole (a regex over the joined
            // line would stop at the space).
            ("mysql\0-pa b\0db", "mysql -p*** db"),
            ("redis-cli\0-a\0p w\0ping", "redis-cli -a *** ping"),
            ("sshpass\0-p\0p w\0ssh", "sshpass -p *** ssh"),
            ("env\0PGPASSWORD=abc\0psql", "env PGPASSWORD=*** psql"),
        ] {
            assert_eq!(redacted(raw), want, "{raw:?}");
        }
    }

    #[test]
    fn leaves_what_is_no_secret() {
        for (raw, want) in [
            // mysql -p alone prompts; -P is the port.
            ("mysql\0-p\0-P3306\0db", "mysql -p -P3306 db"),
            (
                "app\0--token-file\0/etc/app/token",
                "app --token-file /etc/app/token",
            ),
            ("nginx: worker process", "nginx: worker process"),
            ("sleep\x00300", "sleep 300"),
            ("", ""),
            // The last argument a password option: nothing follows to mask.
            ("app\0--password", "app --password"),
        ] {
            assert_eq!(redacted(raw), want, "{raw:?}");
        }
        // -p of other programs is no password.
        assert_eq!(redacted("grep\0-pfoo\0x"), "grep -pfoo x");
    }

    // The usual redaction runs over the joined line: rules may span arguments; a long hash as an
    // argument stays (no line of its own).
    #[test]
    fn redacts_the_joined_line() {
        let hash = "a".repeat(64);
        assert_eq!(
            redacted(&format!("containerd-shim\0-id\0{hash}")),
            format!("containerd-shim -id {hash}")
        );
    }

    // The organization's rules apply too.
    #[test]
    fn applies_the_rules_per_argument() {
        let redactor = Redactor::with_rules(&[crate::RuleSpec {
            pattern: r"customer-\d{6}".into(),
        }])
        .unwrap();
        assert_eq!(
            redact_args("export\0--id\0customer-123456", |a| redactor
                .redact(a)
                .into_owned()),
            "export --id ***"
        );
    }
}
