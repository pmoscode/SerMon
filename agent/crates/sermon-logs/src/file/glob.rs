//! Which log files a glob of the server's configuration names (M3-05, KONZEPT §10.1).
//!
//! The server chooses the patterns, the host decides what may be read:
//!
//! - **Patterns:** absolute paths with `*` and `?` inside a segment (no `**`, no classes); `*`
//!   does not match a leading dot. The literal start of a pattern must lie below an allowed prefix
//!   (`log_file_prefixes` in `agent.toml`, default `/var/log`), so no directory outside is even
//!   listed.
//! - **Every match** is resolved (symlinks included) and checked again: below an allowed prefix,
//!   never below a denied path (the state directory, `/etc/sermon`, `/proc`, `/sys`, `/dev`,
//!   the binary journal files and the login records, M3-28).
//!   Prefixes compare by segment: `/var/log2` is not below `/var/log`. A compromised server so
//!   reads no keys of the agent.
//! - **Rotated and compressed files** (`.1`, `.gz`, `-20261123`, …) are never followed: their
//!   lines were read under the active name, the tail follows a rotation itself.
//! - **Bounds:** at most [`MAX_FILES`] files, [`MAX_DIR_ENTRIES`] entries per listed directory.

use std::fs;
use std::io;
use std::os::unix::fs::MetadataExt;
use std::path::{Component, Path, PathBuf};

use super::tail::Guard;

/// Files followed at most, over all patterns.
pub const MAX_FILES: usize = 64;
/// Entries read per listed directory.
pub const MAX_DIR_ENTRIES: usize = 4096;

/// Paths the agent never reads, whatever the prefixes allow. The journal's own files lie below
/// the default prefix `/var/log`: read raw they would carry every field of every unit
/// (command lines, environment), past the fixed field list of the journal source (M3-28).
pub const ALWAYS_DENIED: [&str; 9] = [
    "/etc/sermon",
    "/proc",
    "/sys",
    "/dev",
    "/var/log/journal",
    "/run/log/journal",
    "/var/log/wtmp",
    "/var/log/btmp",
    "/var/log/lastlog",
];

/// Why a pattern or a file is not followed.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum Rejected {
    /// Not an absolute path with only normal segments.
    #[error("not an absolute path without . and .. segments")]
    Malformed,
    /// Its literal start is not below an allowed prefix.
    #[error("not below an allowed prefix (log_file_prefixes in agent.toml)")]
    Outside,
    /// Below a path the agent never reads.
    #[error("below a path the agent never reads")]
    Denied,
}

/// What the host allows: prefixes and denied paths, as host paths below `host_root`.
#[derive(Debug, Clone)]
pub struct Access {
    host_root: PathBuf,
    allowed: Vec<PathBuf>,
    denied: Vec<PathBuf>,
}

/// The result of expanding the patterns.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Expansion {
    /// Host paths of the files to follow with the index of their pattern, sorted, at most
    /// [`MAX_FILES`].
    pub files: Vec<(PathBuf, usize)>,
    /// Matches not followed: outside the prefixes after resolving, denied, or over the bound.
    pub rejected: usize,
}

impl Access {
    /// `allowed` and `denied` are host paths; `ALWAYS_DENIED` is added to `denied`.
    #[must_use]
    pub fn new(host_root: impl Into<PathBuf>, allowed: &[PathBuf], denied: &[PathBuf]) -> Self {
        let host_root = host_root.into();
        let denied = denied
            .iter()
            .cloned()
            .chain(ALWAYS_DENIED.iter().map(PathBuf::from))
            .collect();
        Self {
            host_root,
            allowed: allowed.to_vec(),
            denied,
        }
    }

    /// Whether a pattern may be followed at all: well formed and its literal start below an
    /// allowed prefix and not below a denied path.
    ///
    /// # Errors
    ///
    /// Why not.
    pub fn check_pattern(&self, pattern: &str) -> Result<(), Rejected> {
        let segments = segments(pattern).ok_or(Rejected::Malformed)?;
        let literal: PathBuf = std::iter::once("/")
            .chain(segments.iter().copied().take_while(|s| !wildcard(s)))
            .collect();
        if !self
            .allowed
            .iter()
            .any(|prefix| literal.starts_with(prefix))
        {
            return Err(Rejected::Outside);
        }
        // A pattern ending above a denied path (e.g. /var/lib/sermon-agent/*) is refused as a
        // whole; matches inside one are refused again after resolving.
        if self.denied.iter().any(|d| literal.starts_with(d)) {
            return Err(Rejected::Denied);
        }
        Ok(())
    }

    /// The files of the patterns; patterns that fail `check_pattern` match nothing.
    #[must_use]
    pub fn expand(&self, patterns: &[&str]) -> Expansion {
        let mut found: Vec<(PathBuf, usize)> = Vec::new();
        let mut rejected = 0;
        // Resolved once per expansion, not per candidate (M3-29).
        let roots = self.roots();
        for (index, pattern) in patterns.iter().enumerate() {
            if self.check_pattern(pattern).is_err() {
                continue;
            }
            let Some(segments) = segments(pattern) else {
                continue;
            };
            let mut candidates = vec![PathBuf::from("/")];
            for (i, segment) in segments.iter().enumerate() {
                let last = i + 1 == segments.len();
                // Bounded, the smallest names kept (M3-29): wildcards in several segments don't
                // multiply, and a huge directory is never held at once.
                let mut next = Smallest::default();
                for dir in &candidates {
                    if wildcard(segment) {
                        for name in self.list(dir) {
                            if matches(segment, &name) && (!last || !rotated(&name)) {
                                next.push(dir.join(name));
                            }
                        }
                    } else {
                        next.push(dir.join(segment));
                    }
                }
                let (kept, dropped) = next.finish();
                if last {
                    rejected += dropped;
                }
                candidates = kept;
            }
            for path in candidates {
                match self.check_resolved(&path, &roots) {
                    Ok(true) => {
                        if !found.iter().any(|(p, _)| p == &path) {
                            found.push((path, index));
                        }
                    }
                    Ok(false) => {}
                    Err(_) => rejected += 1,
                }
            }
        }
        found.sort();
        if found.len() > MAX_FILES {
            rejected += found.len() - MAX_FILES;
            found.truncate(MAX_FILES);
        }
        Expansion {
            files: found,
            rejected,
        }
    }

    /// The names in a host directory, at most [`MAX_DIR_ENTRIES`].
    fn list(&self, dir: &Path) -> Vec<String> {
        let Ok(entries) = fs::read_dir(self.on_host(dir)) else {
            return Vec::new();
        };
        entries
            .take(MAX_DIR_ENTRIES)
            .filter_map(|e| e.ok()?.file_name().into_string().ok())
            .collect()
    }

    /// `Ok(true)` for a regular file that may be read, `Ok(false)` if there is none, an error if
    /// it resolves outside or into a denied path.
    ///
    /// # Errors
    ///
    /// Why it is refused.
    pub fn check_file(&self, path: &Path) -> Result<bool, Rejected> {
        self.check_resolved(path, &self.roots())
    }

    /// The allowed prefixes and denied paths as they resolve on the host (missing ones left out).
    fn roots(&self) -> Roots {
        let resolve = |paths: &[PathBuf]| {
            paths
                .iter()
                .filter_map(|p| fs::canonicalize(self.on_host(p)).ok())
                .collect()
        };
        Roots {
            allowed: resolve(&self.allowed),
            denied: resolve(&self.denied),
        }
    }

    fn check_resolved(&self, path: &Path, roots: &Roots) -> Result<bool, Rejected> {
        let Ok(resolved) = fs::canonicalize(self.on_host(path)) else {
            return Ok(false);
        };
        if !resolved.is_file() {
            return Ok(false);
        }
        if roots.denied.iter().any(|d| resolved.starts_with(d)) {
            return Err(Rejected::Denied);
        }
        if !roots.allowed.iter().any(|p| resolved.starts_with(p)) {
            return Err(Rejected::Outside);
        }
        Ok(true)
    }

    /// The check of [`Access::check_file`] for a host path, as a [`Guard`] of the tail: the
    /// device and inode of the file it may read (M3-28).
    #[must_use]
    pub fn guard(&self) -> Guard {
        let access = self.clone();
        Guard::new(move |on_host: &Path| access.verify(on_host))
    }

    fn verify(&self, on_host: &Path) -> io::Result<(u64, u64)> {
        let resolved = fs::canonicalize(on_host)?;
        let meta = fs::metadata(&resolved)?;
        if !meta.is_file() {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                "not a regular file",
            ));
        }
        let inside = |host: &Path| {
            fs::canonicalize(self.on_host(host)).is_ok_and(|real| resolved.starts_with(real))
        };
        if self.denied.iter().any(|d| inside(d)) || !self.allowed.iter().any(|p| inside(p)) {
            return Err(io::Error::new(
                io::ErrorKind::PermissionDenied,
                "outside the allowed prefixes or below a denied path",
            ));
        }
        Ok((meta.dev(), meta.ino()))
    }

    /// The path below the host root.
    #[must_use]
    pub fn on_host(&self, path: &Path) -> PathBuf {
        self.host_root.join(path.strip_prefix("/").unwrap_or(path))
    }
}

/// Resolved prefixes and denied paths.
struct Roots {
    allowed: Vec<PathBuf>,
    denied: Vec<PathBuf>,
}

/// Candidates of a segment: the smallest [`MAX_FILES`]` * 4` paths in order, held at most twice
/// that many at once; the others are counted.
#[derive(Default)]
struct Smallest {
    paths: Vec<PathBuf>,
    dropped: usize,
}

const fn keep_smallest_bound() -> usize {
    MAX_FILES * 4
}

impl Smallest {
    fn push(&mut self, path: PathBuf) {
        self.paths.push(path);
        if self.paths.len() >= 2 * keep_smallest_bound() {
            self.trim();
        }
    }

    fn trim(&mut self) {
        self.paths.sort();
        self.paths.dedup();
        let bound = keep_smallest_bound();
        if self.paths.len() > bound {
            self.dropped += self.paths.len() - bound;
            self.paths.truncate(bound);
        }
    }

    fn finish(mut self) -> (Vec<PathBuf>, usize) {
        self.trim();
        (self.paths, self.dropped)
    }
}

/// The segments of an absolute pattern; `None` with `.`/`..`, empty segments or a NUL.
fn segments(pattern: &str) -> Option<Vec<&str>> {
    let rest = pattern.strip_prefix('/')?;
    if rest.is_empty() || pattern.contains('\0') {
        return None;
    }
    let segments: Vec<&str> = rest.split('/').collect();
    let normal = segments.iter().all(|s| {
        !s.is_empty()
            && matches!(Path::new(s).components().next(), Some(Component::Normal(_)))
            && *s != "."
            && *s != ".."
    });
    normal.then_some(segments)
}

fn wildcard(segment: &str) -> bool {
    segment.contains(['*', '?'])
}

/// Whether `name` matches a segment with `*` (any run) and `?` (one character); a leading dot
/// must be matched literally.
#[must_use]
pub fn matches(pattern: &str, name: &str) -> bool {
    if name.starts_with('.') && !pattern.starts_with('.') {
        return false;
    }
    let p: Vec<char> = pattern.chars().collect();
    let n: Vec<char> = name.chars().collect();
    // Iterative matching with backtracking to the last star: linear in practice, no recursion.
    let (mut pi, mut ni) = (0, 0);
    let mut star: Option<(usize, usize)> = None;
    while ni < n.len() {
        if pi < p.len() && (p[pi] == '?' || p[pi] == n[ni]) {
            pi += 1;
            ni += 1;
        } else if pi < p.len() && p[pi] == '*' {
            star = Some((pi, ni));
            pi += 1;
        } else if let Some((sp, sn)) = star {
            pi = sp + 1;
            ni = sn + 1;
            star = Some((sp, sn + 1));
        } else {
            return false;
        }
    }
    p[pi..].iter().all(|c| *c == '*')
}

/// Whether a name is that of a rotated or compressed log file.
#[must_use]
pub fn rotated(name: &str) -> bool {
    const COMPRESSED: [&str; 8] = [".gz", ".xz", ".bz2", ".zst", ".lz4", ".zip", ".old", ".bak"];
    if COMPRESSED.iter().any(|s| name.ends_with(s)) {
        return true;
    }
    // access.log.1, messages.0
    if let Some((stem, last)) = name.rsplit_once('.')
        && !stem.is_empty()
        && !last.is_empty()
        && last.bytes().all(|b| b.is_ascii_digit())
    {
        return true;
    }
    // logrotate dateext: syslog-20261123, app.log-2026112312
    name.rsplit_once('-').is_some_and(|(stem, date)| {
        !stem.is_empty() && date.len() >= 8 && date.bytes().all(|b| b.is_ascii_digit())
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::os::unix::fs::symlink;

    struct Host {
        root: tempfile::TempDir,
    }

    impl Host {
        fn new() -> Self {
            Self {
                root: tempfile::tempdir().unwrap(),
            }
        }

        fn file(&self, path: &str) {
            let p = self.root.path().join(path.trim_start_matches('/'));
            fs::create_dir_all(p.parent().unwrap()).unwrap();
            fs::write(p, "x\n").unwrap();
        }

        fn link(&self, path: &str, target: &str) {
            let p = self.root.path().join(path.trim_start_matches('/'));
            fs::create_dir_all(p.parent().unwrap()).unwrap();
            let target = self.root.path().join(target.trim_start_matches('/'));
            symlink(target, p).unwrap();
        }

        fn access(&self) -> Access {
            Access::new(
                self.root.path(),
                &[PathBuf::from("/var/log")],
                &[PathBuf::from("/var/lib/sermon-agent")],
            )
        }
    }

    fn names(e: &Expansion) -> Vec<String> {
        e.files
            .iter()
            .map(|(p, _)| p.display().to_string())
            .collect()
    }

    #[test]
    fn matches_star_and_question_mark_within_a_segment() {
        assert!(matches("*.log", "access.log"));
        assert!(!matches("*.log", ".log"));
        assert!(matches(".*", ".hidden"));
        assert!(matches("app-?.log", "app-1.log"));
        assert!(!matches("app-?.log", "app-12.log"));
        assert!(matches("*", "x"));
        assert!(matches("a*b*c", "a-b-b-c"));
        assert!(!matches("a*b*c", "a-b-b-d"));
        assert!(matches("error.log", "error.log"));
        assert!(!matches("*.log", "access.log.1"));
        // No catastrophic backtracking.
        let long = "a".repeat(10_000);
        assert!(!matches("*a*a*a*a*a*a*b", &long));
    }

    #[test]
    fn knows_rotated_and_compressed_names() {
        for name in [
            "access.log.1",
            "messages.0",
            "syslog.2.gz",
            "app.log.xz",
            "syslog-20261123",
            "app.log-2026112312",
            "x.old",
        ] {
            assert!(rotated(name), "{name}");
        }
        for name in [
            "access.log",
            "error_log",
            "app-1.log",
            "v2.3.log",
            "app.log-x",
        ] {
            assert!(!rotated(name), "{name}");
        }
    }

    #[test]
    fn checks_the_literal_start_of_a_pattern() {
        let access = Host::new().access();
        assert_eq!(access.check_pattern("/var/log/nginx/*.log"), Ok(()));
        assert_eq!(access.check_pattern("/var/log/*/error.log"), Ok(()));
        for (pattern, why) in [
            ("/etc/*", Rejected::Outside),
            ("/*/log/x", Rejected::Outside),
            ("/var/log2/x.log", Rejected::Outside),
            ("/var/lo*/x.log", Rejected::Outside),
            ("var/log/x", Rejected::Malformed),
            ("/var/log/../../etc/shadow", Rejected::Malformed),
            ("/var/log/./x", Rejected::Malformed),
            ("/var/log//x", Rejected::Malformed),
            ("/", Rejected::Malformed),
        ] {
            assert_eq!(access.check_pattern(pattern), Err(why), "{pattern}");
        }
        let wide = Access::new(
            "/",
            &[PathBuf::from("/")],
            &[PathBuf::from("/var/lib/sermon-agent")],
        );
        for pattern in [
            "/var/lib/sermon-agent/*",
            "/etc/sermon/agent.toml",
            "/proc/*/environ",
            "/dev/*",
        ] {
            assert_eq!(
                wide.check_pattern(pattern),
                Err(Rejected::Denied),
                "{pattern}"
            );
        }
    }

    #[test]
    fn expands_globs_to_the_active_files() {
        let host = Host::new();
        for f in [
            "/var/log/nginx/access.log",
            "/var/log/nginx/error.log",
            "/var/log/nginx/access.log.1",
            "/var/log/nginx/access.log.2.gz",
            "/var/log/nginx/.hidden.log",
            "/var/log/app/a/server.log",
            "/var/log/app/b/server.log",
        ] {
            host.file(f);
        }
        fs::create_dir_all(host.root.path().join("var/log/nginx/dir.log")).unwrap();
        let e = host.access().expand(&[
            "/var/log/nginx/*",
            "/var/log/app/*/server.log",
            "/var/log/nginx/error.log",
        ]);
        assert_eq!(
            names(&e),
            [
                "/var/log/app/a/server.log",
                "/var/log/app/b/server.log",
                "/var/log/nginx/access.log",
                "/var/log/nginx/error.log",
            ]
        );
        // The first pattern naming a file is its pattern.
        assert_eq!(e.files[3].1, 0);
        assert_eq!(e.rejected, 0);
    }

    #[test]
    fn refuses_files_that_resolve_outside_or_into_denied_paths() {
        let host = Host::new();
        host.file("/var/lib/sermon-agent/identity.key");
        host.file("/etc/shadow");
        host.file("/var/log2/x.log");
        host.file("/var/log/ok.log");
        host.link("/var/log/key.log", "/var/lib/sermon-agent/identity.key");
        host.link("/var/log/shadow.log", "/etc/shadow");
        host.link("/var/log/trick.log", "/var/log2/x.log");
        host.link("/var/log/inside.log", "/var/log/ok.log");
        let e = host.access().expand(&["/var/log/*.log"]);
        assert_eq!(names(&e), ["/var/log/inside.log", "/var/log/ok.log"]);
        assert_eq!(e.rejected, 3);
        assert_eq!(
            host.access().check_file(Path::new("/var/log/key.log")),
            Err(Rejected::Denied)
        );
        assert_eq!(
            host.access().check_file(Path::new("/var/log/shadow.log")),
            Err(Rejected::Outside)
        );
        assert_eq!(
            host.access().check_file(Path::new("/var/log/trick.log")),
            Err(Rejected::Outside)
        );
        // A prefix that allows the state directory still doesn't read it.
        let wide = Access::new(
            host.root.path(),
            &[PathBuf::from("/var")],
            &[PathBuf::from("/var/lib/sermon-agent")],
        );
        assert_eq!(
            wide.check_file(Path::new("/var/log/key.log")),
            Err(Rejected::Denied)
        );
        // A directory symlinked out.
        host.link("/var/log/etc", "/etc");
        assert_eq!(
            names(&host.access().expand(&["/var/log/etc/*"])),
            Vec::<String>::new()
        );
    }

    #[test]
    fn stays_within_its_bounds() {
        let host = Host::new();
        for i in 0..100 {
            host.file(&format!("/var/log/many/f{i:03}.log"));
        }
        let e = host.access().expand(&["/var/log/many/*.log"]);
        assert_eq!(e.files.len(), MAX_FILES);
        assert_eq!(e.rejected, 100 - MAX_FILES);
        assert_eq!(e.files[0].0, PathBuf::from("/var/log/many/f000.log"));
    }

    // M3-29: a directory with far more matches than followed files: the same smallest names,
    // counted as rejected – without holding every match at once.
    #[test]
    fn far_more_matches_keep_the_smallest_names() {
        let host = Host::new();
        for i in (0..600).rev() {
            host.file(&format!("/var/log/huge/f{i:03}.log"));
        }
        let e = host.access().expand(&["/var/log/huge/*.log"]);
        assert_eq!(e.files.len(), MAX_FILES);
        assert_eq!(e.rejected, 600 - MAX_FILES);
        assert_eq!(e.files[0].0, PathBuf::from("/var/log/huge/f000.log"));
        assert_eq!(
            e.files[MAX_FILES - 1].0,
            PathBuf::from(format!("/var/log/huge/f{:03}.log", MAX_FILES - 1))
        );
        assert_eq!(keep_smallest_bound(), MAX_FILES * 4);
    }

    // M3-28: the journal's own files and the login records lie below /var/log but are never
    // followed – by pattern or by match.
    #[test]
    fn never_follows_the_journal_files_or_login_records() {
        let host = Host::new();
        host.file("/var/log/journal/abc/system.journal");
        host.file("/var/log/wtmp");
        host.file("/var/log/btmp");
        host.file("/var/log/app.log");
        let access = host.access();
        assert_eq!(
            access.check_pattern("/var/log/journal/*/*.journal"),
            Err(Rejected::Denied)
        );
        let e = access.expand(&["/var/log/*/*/*.journal", "/var/log/*"]);
        assert_eq!(names(&e), ["/var/log/app.log"]);
        assert_eq!(e.rejected, 3, "journal file, wtmp and btmp");
    }

    // The guard of a tail checks a host path as the expansion does and names the file's inode.
    #[test]
    fn the_guard_checks_every_open() {
        let host = Host::new();
        host.file("/var/log/app.log");
        host.file("/var/lib/sermon-agent/key");
        host.link("/var/log/evil.log", "/var/lib/sermon-agent/key");
        let access = host.access();
        let guard = access.guard();
        let app = access.on_host(Path::new("/var/log/app.log"));
        let meta = fs::metadata(&app).unwrap();
        assert_eq!(access.verify(&app).unwrap(), (meta.dev(), meta.ino()));
        let evil = access.on_host(Path::new("/var/log/evil.log"));
        assert_eq!(
            access.verify(&evil).unwrap_err().kind(),
            io::ErrorKind::PermissionDenied
        );
        let missing = access.on_host(Path::new("/var/log/none.log"));
        assert_eq!(
            access.verify(&missing).unwrap_err().kind(),
            io::ErrorKind::NotFound
        );
        let mut tail = super::super::tail::Tail::guarded(&evil, None, true, guard);
        let mut out = Vec::new();
        assert!(tail.read(&mut out).is_err() && out.is_empty());
    }
}
