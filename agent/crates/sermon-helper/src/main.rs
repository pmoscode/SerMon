//! `SerMon` root helper (ADR 0035, KONZEPT §4.2): takes a fixed set of operations from the agent
//! over `/run/sermon/helper.sock` – only from the user `sermon` – and does them as root, unless
//! `/etc/sermon/helper.toml` forbids them. Every request leaves a line in the journal.
//!
//! - `sermon-helper serve`: under systemd with the socket of `sermon-helper.socket` (ending after
//!   [`IDLE`] without requests), under OpenRC with a socket of its own.
//! - The package operations themselves follow with M4-03 ([`request::Executor`]).
// Only through `sermon_helper::log`: eprintln! panics when nobody reads the log (M4-31).
#![deny(clippy::print_stderr)]

use std::fs;
use std::io;
use std::os::unix::fs::{FileTypeExt, PermissionsExt};
use std::os::unix::net::{UnixListener, UnixStream};
use std::path::Path;
use std::process::ExitCode;
use std::time::{Duration, Instant};

use prost::Message;
use sermon_helper_proto::v1::{HelperRequest, HelperResponse, Status};

use sermon_helper::config::{self, Config};
use sermon_helper::host::Host;
use sermon_helper::request::{self, Executor};
use sermon_helper::{peer, wire};

const NAME: &str = "sermon-helper";
const USAGE: &str = "usage: sermon-helper --version | serve | run RUN_ID";
/// The socket of the agent.
const SOCKET: &str = "/run/sermon/helper.sock";
/// The time between two connections.
const PACE: Duration = Duration::from_millis(50);
/// Without a request for so long, a socket-activated helper ends; systemd starts it again.
const IDLE: Duration = Duration::from_secs(60);

fn main() -> ExitCode {
    let args: Vec<String> = std::env::args().skip(1).collect();
    match args.as_slice() {
        [flag] if flag == "--version" || flag == "-V" => {
            println!("{NAME} {}", sermon_helper::VERSION);
            ExitCode::SUCCESS
        }
        [cmd] if cmd == "serve" => match serve() {
            Ok(()) => ExitCode::SUCCESS,
            Err(e) => {
                sermon_helper::log(&format!("{NAME}: {e}"));
                ExitCode::FAILURE
            }
        },
        // A run started by serve, apart from it (ADR 0035); only root can read its request.
        [cmd, id] if cmd == "run" && request::valid_run_id(id) => {
            match Host::system().execute(id) {
                Ok(()) => ExitCode::SUCCESS,
                Err(e) => {
                    sermon_helper::log(&format!("{NAME}: run {id}: {e}"));
                    ExitCode::FAILURE
                }
            }
        }
        _ => {
            sermon_helper::log(USAGE);
            ExitCode::from(2)
        }
    }
}

fn serve() -> Result<(), String> {
    let config = config::load(Path::new(config::PATH)).map_err(|e| e.to_string())?;
    let passwd = fs::read_to_string("/etc/passwd").map_err(|e| format!("/etc/passwd: {e}"))?;
    let (uid, gid) = peer::ids_of(&passwd, peer::USER)
        .ok_or_else(|| format!("no user {} in /etc/passwd", peer::USER))?;
    let (listener, activated) = listen(gid).map_err(|e| format!("socket {SOCKET}: {e}"))?;
    sermon_helper::log(&format!(
        "{NAME}: serving {SOCKET} for uid {uid}; {config:?}"
    ));
    let host = Host::system();
    host.prune();
    let mut server = Server {
        config: Box::new(|| config::load(Path::new(config::PATH)).map_err(|e| e.to_string())),
        allowed_uid: uid,
        exec: Box::new(host),
    };
    loop {
        // A self-update replaced the binary (V1-06): end, systemd or supervise-daemon starts the new.
        if activated && !readable_within(&listener, IDLE) || replaced(Path::new("/proc/self/exe")) {
            return Ok(());
        }
        match listener.accept() {
            Ok((stream, _)) => {
                let peer = peer::peer_uid(&stream);
                // The caller's PID too: the agent and a child of it running as sermon differ.
                let pid = peer::peer_pid(&stream).map_or_else(|_| "-".into(), |p| p.to_string());
                sermon_helper::log(&format!(
                    "{NAME}: peer_pid={pid} {}",
                    server.connection(stream, peer)
                ));
                // At most 20 requests a second: a looping agent can't flood the journal and push
                // the lines of real runs out of its rate limit (review M4-04).
                std::thread::sleep(PACE);
            }
            Err(e) => sermon_helper::log(&format!("{NAME}: accept: {e}")),
        }
    }
}

/// Reads helper.toml.
type ConfigSource = Box<dyn FnMut() -> Result<Config, String>>;

/// The helper's state across connections.
struct Server {
    /// helper.toml, read again for every request (review M4-30): a veto applies to the next
    /// request, even if the agent keeps the socket-activated helper running.
    config: ConfigSource,
    allowed_uid: u32,
    exec: Box<dyn Executor>,
}

impl Server {
    /// Serves one connection and returns its line for the journal. A foreign peer gets nothing,
    /// not even a read of its request.
    fn connection(&mut self, mut stream: UnixStream, peer: io::Result<u32>) -> String {
        let uid = match peer {
            Ok(uid) if uid == self.allowed_uid => uid,
            Ok(uid) => {
                return format!(
                    "refused a connection of uid {uid}: only {} may call",
                    peer::USER
                );
            }
            Err(e) => return format!("refused a connection without credentials: {e}"),
        };
        let deadline = Instant::now() + wire::REQUEST_TIME;
        let request = match wire::read_frame(&mut stream, wire::MAX_REQUEST, deadline) {
            Ok(body) => HelperRequest::decode(body.as_slice()),
            Err(wire::ReadError::TooLarge(n)) => {
                let line = format!("peer_uid={uid} status=refused reason=\"request of {n} bytes\"");
                return Self::reply(&mut stream, refusal("request too large"), line);
            }
            Err(wire::ReadError::Io(e)) => return format!("peer_uid={uid} no request: {e}"),
        };
        let Ok(request) = request else {
            let line = format!("peer_uid={uid} status=refused reason=\"malformed request\"");
            return Self::reply(&mut stream, refusal("malformed request"), line);
        };
        // Fail closed: without a readable, safe helper.toml nothing is answered.
        let config = match (self.config)() {
            Ok(c) => c,
            Err(e) => {
                let line = format!(
                    "peer_uid={uid} status=refused reason={:?}",
                    format!("helper.toml: {e}")
                );
                return Self::reply(&mut stream, refusal(&format!("helper.toml: {e}")), line);
            }
        };
        let (response, audit) = request::handle(&request, config, self.exec.as_mut());
        let line = format!(
            "peer_uid={uid} op={}{}{} run_id={} status={} reason={:?}",
            audit.operation,
            if audit.params.is_empty() { "" } else { " " },
            audit.params,
            if audit.run_id.is_empty() {
                "-"
            } else {
                &audit.run_id
            },
            audit
                .status
                .as_str_name()
                .trim_start_matches("STATUS_")
                .to_lowercase(),
            audit.reason
        );
        Self::reply(&mut stream, response, line)
    }

    fn reply(stream: &mut UnixStream, response: HelperResponse, line: String) -> String {
        let mut body = response.encode_to_vec();
        if body.len() > wire::MAX_RESPONSE {
            body = HelperResponse {
                status: Status::Failed.into(),
                reason: "answer too large".into(),
                result: None,
                ..response
            }
            .encode_to_vec();
        }
        match wire::write_frame(stream, &body) {
            Ok(()) => line,
            Err(e) => format!("{line} (answer not sent: {e})"),
        }
    }
}

fn refusal(reason: &str) -> HelperResponse {
    HelperResponse {
        status: Status::Refused.into(),
        reason: reason.to_owned(),
        helper_version: sermon_helper::VERSION.to_owned(),
        result: None,
    }
}

/// The socket from systemd, or one of our own; true if from systemd.
fn listen(gid: u32) -> io::Result<(UnixListener, bool)> {
    let pid = std::process::id();
    match activation(
        std::env::var("LISTEN_PID").ok().as_deref(),
        std::env::var("LISTEN_FDS").ok().as_deref(),
        pid,
    ) {
        Ok(true) => Ok((inherited()?, true)),
        Ok(false) => Ok((bind(Path::new(SOCKET), gid)?, false)),
        Err(why) => Err(io::Error::other(why)),
    }
}

/// Whether systemd passed exactly one socket to this process (`sd_listen_fds`).
fn activation(
    listen_pid: Option<&str>,
    listen_fds: Option<&str>,
    pid: u32,
) -> Result<bool, String> {
    match (listen_pid, listen_fds) {
        (None, None) => Ok(false),
        (Some(p), Some("1")) if p.parse() == Ok(pid) => Ok(true),
        (Some(p), _) if p.parse() != Ok(pid) => Ok(false), // meant for another process
        _ => Err("systemd must pass exactly one socket (sermon-helper.socket)".to_owned()),
    }
}

/// File descriptor 3, the first one systemd passes.
#[allow(
    unsafe_code,
    reason = "taking over the socket systemd passed has no safe API (ADR 0035)"
)]
fn inherited() -> io::Result<UnixListener> {
    use std::os::fd::{FromRawFd, OwnedFd};
    // SAFETY: systemd set LISTEN_PID to this process and LISTEN_FDS to 1 (checked by
    // `activation`), so fd 3 is open, belongs to us alone and nothing else owns it in this
    // process; it is taken over once, at start.
    let fd = unsafe { OwnedFd::from_raw_fd(3) };
    // systemd passes it inheritable: apt, dnf and systemd-run must not get it (review M4-04).
    rustix::io::fcntl_setfd(&fd, rustix::io::FdFlags::CLOEXEC)?;
    let stat = rustix::fs::fstat(&fd)?;
    #[cfg(target_os = "linux")]
    let unix_stream = rustix::net::sockopt::socket_domain(&fd)? == rustix::net::AddressFamily::UNIX
        && rustix::net::sockopt::socket_type(&fd)? == rustix::net::SocketType::STREAM
        && rustix::net::sockopt::socket_acceptconn(&fd)?;
    #[cfg(not(target_os = "linux"))]
    let unix_stream = rustix::net::sockopt::socket_type(&fd)? == rustix::net::SocketType::STREAM;
    if rustix::fs::FileType::from_raw_mode(stat.st_mode) != rustix::fs::FileType::Socket
        || !unix_stream
    {
        return Err(io::Error::other("fd 3 is no listening Unix stream socket"));
    }
    Ok(UnixListener::from(fd))
}

/// Binds the socket with the rights `root:sermon 0660` (OpenRC): created under a umask, so it is
/// never more open than that, and an old socket is replaced (nothing else).
fn bind(path: &Path, gid: u32) -> io::Result<UnixListener> {
    let dir = path.parent().unwrap_or(Path::new("/run"));
    // A link in the directory's place would hand its permissions elsewhere (M4-31).
    match fs::symlink_metadata(dir) {
        Ok(m) if !m.is_dir() => {
            return Err(io::Error::other("the socket's directory is no directory"));
        }
        Ok(_) => {}
        Err(e) if e.kind() == io::ErrorKind::NotFound => fs::create_dir_all(dir)?,
        Err(e) => return Err(e),
    }
    fs::set_permissions(dir, fs::Permissions::from_mode(0o750))?;
    rustix::fs::chown(dir, None, Some(rustix::process::Gid::from_raw(gid)))?;
    match fs::symlink_metadata(path) {
        Ok(m) if m.file_type().is_socket() => fs::remove_file(path)?,
        Ok(_) => return Err(io::Error::other("the path exists and is no socket")),
        Err(e) if e.kind() == io::ErrorKind::NotFound => {}
        Err(e) => return Err(e),
    }
    let old = rustix::process::umask(rustix::fs::Mode::from_raw_mode(0o117));
    let listener = UnixListener::bind(path);
    rustix::process::umask(old);
    let listener = listener?;
    rustix::fs::chown(path, None, Some(rustix::process::Gid::from_raw(gid)))?;
    Ok(listener)
}

/// Whether the binary of this process was replaced on disk (Linux marks the old one deleted).
fn replaced(exe: &Path) -> bool {
    fs::read_link(exe).is_ok_and(|p| p.to_string_lossy().ends_with(" (deleted)"))
}

/// Whether a connection waits within `idle`.
fn readable_within(listener: &UnixListener, idle: Duration) -> bool {
    use rustix::event::{PollFd, PollFlags, Timespec};
    let timeout = Timespec {
        tv_sec: i64::try_from(idle.as_secs()).unwrap_or(i64::MAX),
        tv_nsec: 0,
    };
    let mut fds = [PollFd::new(listener, PollFlags::IN)];
    !matches!(rustix::event::poll(&mut fds, Some(&timeout)), Ok(0))
}

#[cfg(test)]
mod tests {
    // V1-06: a self-update replaces the binary under a running helper; the old process must end so
    // the next connection starts the new one (it answered the agent's status with the old version).
    #[test]
    fn notices_that_its_binary_was_replaced() {
        let dir = tempfile::tempdir().unwrap();
        let link = dir.path().join("exe");
        std::os::unix::fs::symlink("/usr/local/bin/sermon-helper", &link).unwrap();
        assert!(!replaced(&link));
        fs::remove_file(&link).unwrap();
        std::os::unix::fs::symlink("/usr/local/bin/sermon-helper (deleted)", &link).unwrap();
        assert!(replaced(&link));
        assert!(
            !replaced(&dir.path().join("none")),
            "no /proc: keep serving"
        );
    }

    use sermon_helper_proto::v1::{ApplyUpdates, GetStatus, Scope, helper_request::Operation};

    use super::*;

    const UID: u32 = 997;

    /// An executor that does nothing: the connection's rules are under test, not the work.
    struct Nothing;

    impl Executor for Nothing {
        fn status(&self) -> sermon_helper_proto::v1::StatusInfo {
            sermon_helper_proto::v1::StatusInfo::default()
        }
        fn list_updates(
            &mut self,
            _: bool,
        ) -> Result<sermon_helper_proto::v1::Updates, request::Failure> {
            Err(request::Failure::Busy)
        }
        fn start(&mut self, _: &request::Run) -> Result<(), request::Failure> {
            Err(request::Failure::Busy)
        }
        fn result(&self, _: &str) -> sermon_helper_proto::v1::RunState {
            sermon_helper_proto::v1::RunState::default()
        }
        fn confirm(&mut self, _: &str) -> Result<(), request::Failure> {
            Err(request::Failure::Busy)
        }
        fn read_smart(&mut self) -> Result<sermon_helper_proto::v1::Smart, request::Failure> {
            Err(request::Failure::Busy)
        }
    }

    fn server(config: Config) -> Server {
        Server {
            config: Box::new(move || Ok(config)),
            allowed_uid: UID,
            exec: Box::new(Nothing),
        }
    }

    /// Sends `body` as one frame from a client and returns the answer and the journal line.
    fn call(
        server: &mut Server,
        body: &[u8],
        peer: io::Result<u32>,
    ) -> (Option<HelperResponse>, String) {
        let (mut client, conn) = UnixStream::pair().unwrap();
        // From another thread: a large request doesn't fit into the socket's buffer.
        let mut writer = client.try_clone().unwrap();
        let body = body.to_vec();
        let sent = std::thread::spawn(move || wire::write_frame(&mut writer, &body));
        let line = server.connection(conn, peer);
        let _ = sent.join().unwrap();
        let answer = wire::read_frame(
            &mut client,
            wire::MAX_RESPONSE,
            Instant::now() + Duration::from_secs(1),
        )
        .ok()
        .map(|b| HelperResponse::decode(b.as_slice()).unwrap());
        (answer, line)
    }

    fn request(op: Operation) -> Vec<u8> {
        HelperRequest {
            operation: Some(op),
        }
        .encode_to_vec()
    }

    #[test]
    fn answers_the_agent_and_logs_each_request() {
        let mut s = server(Config::default());
        let (r, line) = call(
            &mut s,
            &request(Operation::GetStatus(GetStatus {})),
            Ok(UID),
        );
        let r = r.unwrap();
        assert_eq!(r.status(), Status::Ok);
        assert_eq!(
            line,
            "peer_uid=997 op=get_status run_id=- status=ok reason=\"\""
        );
        let apply = Operation::ApplyUpdates(ApplyUpdates {
            run_id: "0193f5a0-0000-7000-8000-000000000001".into(),
            scope: Scope::All.into(),
        });
        let mut vetoed = server(Config {
            allow_remote_updates: false,
            ..Config::default()
        });
        let (r, line) = call(&mut vetoed, &request(apply), Ok(UID));
        assert_eq!(r.unwrap().status(), Status::Refused);
        assert!(line.starts_with("peer_uid=997 op=apply_updates scope=all run_id=0193f5a0-0000-7000-8000-000000000001 status=refused reason=\"allow_remote_updates = false"), "{line}");
    }

    // The veto applies to the next request (review M4-30); without a readable helper.toml the
    // helper answers nothing but the refusal.
    #[test]
    fn reads_the_veto_for_every_request() {
        let allowed = std::rc::Rc::new(std::cell::Cell::new(true));
        let seen = allowed.clone();
        let mut s = Server {
            config: Box::new(move || {
                Ok(Config {
                    allow_remote_updates: seen.get(),
                    ..Config::default()
                })
            }),
            allowed_uid: UID,
            exec: Box::new(Nothing),
        };
        let apply = || {
            request(Operation::ApplyUpdates(ApplyUpdates {
                run_id: "0193f5a0-0000-7000-8000-000000000001".into(),
                scope: Scope::All.into(),
            }))
        };
        // Allowed: the executor is asked (and is busy).
        let (r, _) = call(&mut s, &apply(), Ok(UID));
        assert_eq!(r.unwrap().status(), Status::Busy);
        allowed.set(false);
        let (r, line) = call(&mut s, &apply(), Ok(UID));
        assert_eq!(r.unwrap().status(), Status::Refused);
        assert!(line.contains("allow_remote_updates = false"), "{line}");
        let mut broken = Server {
            config: Box::new(|| Err("not owned by root".into())),
            allowed_uid: UID,
            exec: Box::new(Nothing),
        };
        let (r, line) = call(
            &mut broken,
            &request(Operation::GetStatus(GetStatus {})),
            Ok(UID),
        );
        let r = r.unwrap();
        assert_eq!(r.status(), Status::Refused);
        assert_eq!(r.reason, "helper.toml: not owned by root");
        assert!(line.contains("status=refused"), "{line}");
    }

    // Other users, root included, get no answer: their request is not even read.
    #[test]
    fn refuses_other_users_without_reading() {
        let mut s = server(Config::default());
        for (peer, logged) in [
            (Ok(0), "uid 0"),
            (Ok(1000), "uid 1000"),
            (
                Err(io::ErrorKind::Unsupported.into()),
                "without credentials",
            ),
        ] {
            let (r, line) = call(&mut s, &request(Operation::GetStatus(GetStatus {})), peer);
            assert!(r.is_none(), "answered {r:?}");
            assert!(line.contains(logged), "{line}");
        }
    }

    #[test]
    fn refuses_large_and_malformed_requests() {
        let mut s = server(Config::default());
        let (r, line) = call(&mut s, &vec![0; wire::MAX_REQUEST + 1], Ok(UID));
        assert_eq!(r.unwrap().reason, "request too large");
        assert!(line.contains("status=refused"));
        let (r, _) = call(&mut s, &[0xff, 0xff, 0xff], Ok(UID));
        assert_eq!(r.unwrap().reason, "malformed request");
        // Empty: no operation known – an old helper meeting a new agent.
        let (r, line) = call(&mut s, &[], Ok(UID));
        assert_eq!(r.unwrap().status(), Status::Unsupported);
        assert!(line.contains("op=unknown"));
    }

    #[test]
    fn takes_only_its_own_socket_from_systemd() {
        assert_eq!(activation(None, None, 42), Ok(false));
        assert_eq!(activation(Some("42"), Some("1"), 42), Ok(true));
        assert_eq!(activation(Some("41"), Some("1"), 42), Ok(false));
        assert!(activation(Some("42"), Some("2"), 42).is_err());
        assert!(activation(Some("42"), None, 42).is_err());
        assert!(activation(None, Some("1"), 42).is_err());
    }

    #[test]
    fn binds_a_socket_only_the_group_can_use() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("run/helper.sock");
        let gid = rustix::process::getgid().as_raw();
        let first = bind(&path, gid).unwrap();
        let mode = fs::metadata(&path).unwrap().permissions().mode() & 0o777;
        assert_eq!(mode, 0o660);
        assert_eq!(
            fs::metadata(path.parent().unwrap())
                .unwrap()
                .permissions()
                .mode()
                & 0o777,
            0o750
        );
        drop(first);
        // An old socket is replaced, a file is not.
        assert!(bind(&path, gid).is_ok());
        let file = dir.path().join("file.sock");
        fs::write(&file, "x").unwrap();
        assert!(bind(&file, gid).is_err());
        assert!(fs::metadata(&file).unwrap().is_file());
        // M4-31: a link in the directory's place: its target keeps its permissions.
        let target = dir.path().join("target");
        fs::create_dir(&target).unwrap();
        fs::set_permissions(&target, fs::Permissions::from_mode(0o700)).unwrap();
        std::os::unix::fs::symlink(&target, dir.path().join("linked")).unwrap();
        assert!(bind(&dir.path().join("linked/helper.sock"), gid).is_err());
        assert_eq!(
            fs::metadata(&target).unwrap().permissions().mode() & 0o777,
            0o700
        );
    }
}
