//! One fixed command as root (ADR 0035): a fixed environment, no input, its own process group
//! (a time limit ends the whole group, dpkg included) and only the end of its output kept.

use std::collections::VecDeque;
use std::io::Read;
use std::path::Path;
use std::process::{Child, Command, Stdio};
use std::sync::{Arc, Mutex, mpsc};
use std::thread;
use std::time::{Duration, Instant};

use sermon_helper_proto::v1::Command as Output;

/// What the package managers see, nothing from the caller's environment.
const ENV: [(&str, &str); 7] = [
    ("PATH", "/usr/sbin:/usr/bin:/sbin:/bin"),
    ("LC_ALL", "C"),
    ("LANG", "C"),
    ("HOME", "/root"),
    ("DEBIAN_FRONTEND", "noninteractive"),
    ("APT_LISTCHANGES_FRONTEND", "none"),
    // needrestart (Ubuntu) only lists services to restart instead of asking.
    ("NEEDRESTART_MODE", "l"),
];

/// How long the output may still come after the command ended: a daemon started by a package's
/// script may hold the pipe open for ever.
const GRACE: Duration = Duration::from_secs(2);

/// The end of the output so far, and whether something before it was dropped.
type Tail = Arc<Mutex<(VecDeque<u8>, bool)>>;

/// Runs `program` with `args` for at most `limit` and keeps the last `keep` bytes of its output
/// (standard error too if `with_stderr`). Killed at the limit, the exit code is -1.
#[must_use]
pub fn run(
    program: &Path,
    args: &[&str],
    limit: Duration,
    keep: usize,
    with_stderr: bool,
) -> Output {
    let name = std::iter::once(program.display().to_string())
        .chain(args.iter().map(|a| (*a).to_owned()))
        .collect::<Vec<_>>()
        .join(" ");
    let mut command = Command::new(program);
    command
        .args(args)
        .env_clear()
        .envs(ENV)
        .current_dir("/")
        .stdin(Stdio::null())
        .stdout(Stdio::piped());
    command.stderr(if with_stderr {
        Stdio::piped()
    } else {
        Stdio::null()
    });
    std::os::unix::process::CommandExt::process_group(&mut command, 0);
    let mut child = match command.spawn() {
        Ok(c) => c,
        Err(e) => {
            return Output {
                name,
                exit_code: -1,
                output: e.to_string().into_bytes(),
                truncated: false,
            };
        }
    };
    let buffer: Tail = Arc::default();
    let (done, finished) = mpsc::channel();
    let readers = usize::from(reader(child.stdout.take(), &buffer, &done, keep))
        + usize::from(reader(child.stderr.take(), &buffer, &done, keep));
    let exit_code = wait(&mut child, limit);
    let grace = Instant::now() + GRACE;
    for _ in 0..readers {
        if finished
            .recv_timeout(grace.saturating_duration_since(Instant::now()))
            .is_err()
        {
            break;
        }
    }
    let (output, truncated) = buffer
        .lock()
        .map_or_else(|_| (Vec::new(), false), |b| (Vec::from(b.0.clone()), b.1));
    Output {
        name,
        exit_code,
        output,
        truncated,
    }
}

/// The exit code, or -1 after killing the process group at the limit.
fn wait(child: &mut Child, limit: Duration) -> i32 {
    let deadline = Instant::now() + limit;
    loop {
        match child.try_wait() {
            Ok(Some(status)) => return status.code().unwrap_or(-1),
            Ok(None) if Instant::now() < deadline => thread::sleep(Duration::from_millis(50)),
            _ => {
                if let Some(pid) =
                    rustix::process::Pid::from_raw(i32::try_from(child.id()).unwrap_or(0))
                {
                    let _ = rustix::process::kill_process_group(pid, rustix::process::Signal::KILL);
                }
                let _ = child.wait();
                return -1;
            }
        }
    }
}

/// Reads `stream` into `buffer` in a thread of its own and says so on `done` at its end; false
/// without a stream.
fn reader<R: Read + Send + 'static>(
    stream: Option<R>,
    buffer: &Tail,
    done: &mpsc::Sender<()>,
    keep: usize,
) -> bool {
    let Some(stream) = stream else { return false };
    let (buffer, done) = (buffer.clone(), done.clone());
    thread::spawn(move || {
        tail(stream, &buffer, keep);
        let _ = done.send(());
    });
    true
}

/// Keeps the last `keep` bytes of a stream (both streams in one buffer) in `buffer`.
fn tail(mut r: impl Read, buffer: &Tail, keep: usize) {
    let mut chunk = [0u8; 8192];
    while let Ok(n @ 1..) = r.read(&mut chunk) {
        let Ok(mut b) = buffer.lock() else { return };
        b.0.extend(&chunk[..n]);
        if b.0.len() > keep {
            let cut = b.0.len() - keep;
            b.0.drain(..cut);
            b.1 = true;
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn sh(script: &str, limit: Duration, keep: usize) -> Output {
        run(Path::new("/bin/sh"), &["-c", script], limit, keep, true)
    }

    #[test]
    fn keeps_the_exit_code_and_the_output() {
        let o = sh(
            "echo out; echo err >&2; exit 100",
            Duration::from_secs(5),
            1024,
        );
        assert_eq!(o.exit_code, 100);
        let text = String::from_utf8(o.output).unwrap();
        assert!(text.contains("out\n") && text.contains("err\n"), "{text}");
        assert!(!o.truncated);
        assert_eq!(o.name, "/bin/sh -c echo out; echo err >&2; exit 100");
        // Without standard error only the output counts (lists the agent parses).
        let o = run(
            Path::new("/bin/sh"),
            &["-c", "echo out; echo err >&2"],
            Duration::from_secs(5),
            1024,
            false,
        );
        assert_eq!(o.output, b"out\n");
    }

    #[test]
    fn sees_a_fixed_environment_only() {
        let o = sh("env | sort", Duration::from_secs(5), 4096);
        let env = String::from_utf8(o.output).unwrap();
        assert!(
            env.contains("DEBIAN_FRONTEND=noninteractive\n") && env.contains("LC_ALL=C\n"),
            "{env}"
        );
        assert!(
            !env.contains("CARGO") && !env.contains("RUST"),
            "inherited: {env}"
        );
        let o = sh("pwd; cat", Duration::from_secs(5), 64);
        assert_eq!(o.output, b"/\n", "no input");
    }

    #[test]
    fn keeps_only_the_end_of_long_output() {
        let o = sh(
            "head -c 300000 /dev/zero | tr '\\0' a; printf END",
            Duration::from_secs(10),
            1024,
        );
        assert_eq!(o.output.len(), 1024);
        assert!(o.truncated && o.output.ends_with(b"aEND"));
    }

    // At the limit the whole process group ends – children like dpkg too.
    #[test]
    fn kills_the_group_at_the_limit() {
        let dir = tempfile::tempdir().unwrap();
        let marker = dir.path().join("child-lived");
        let started = Instant::now();
        let o = sh(
            &format!("(sleep 2; touch {}) & sleep 30", marker.display()),
            Duration::from_millis(300),
            64,
        );
        assert_eq!(o.exit_code, -1);
        assert!(started.elapsed() < Duration::from_secs(2));
        thread::sleep(Duration::from_millis(2500));
        assert!(!marker.exists(), "a child survived the limit");
    }

    // A daemon started by a package's script keeps the pipe open: the run still ends.
    #[test]
    fn ends_although_a_daemon_holds_the_output() {
        let started = Instant::now();
        let o = sh("sleep 30 & echo started", Duration::from_secs(10), 64);
        assert_eq!(
            (o.exit_code, o.output.as_slice()),
            (0, b"started\n".as_slice())
        );
        assert!(
            started.elapsed() < GRACE + Duration::from_secs(2),
            "{:?}",
            started.elapsed()
        );
    }

    #[test]
    fn a_missing_program_fails_without_panicking() {
        let o = run(
            Path::new("/nonexistent/apt-get"),
            &[],
            Duration::from_secs(1),
            64,
            true,
        );
        assert_eq!(o.exit_code, -1);
        assert_ne!(o.output, b"");
    }
}
