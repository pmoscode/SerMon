//! The root helper's client (ADR 0035): one length-prefixed protobuf request per connection over
//! `/run/sermon/helper.sock`. The helper answers only the user `sermon`; everyone else finds the
//! socket closed to them (0660) or gets no answer (root, `SO_PEERCRED`).

use std::fmt;
use std::io::{self, Read, Write};
use std::os::unix::net::UnixStream;
use std::path::Path;
use std::time::Duration;

use prost::Message;
use sermon_helper_proto::v1::{
    Distribution, GetStatus, HelperRequest, HelperResponse, Status, helper_request::Operation,
    helper_response::Result as Answer,
};

/// The helper's socket.
pub const SOCKET: &str = "/run/sermon/helper.sock";
/// Largest answer read (the helper sends at most this).
pub const MAX_RESPONSE: usize = 8 * 1024 * 1024;

/// Why the helper gave no answer.
#[derive(Debug)]
pub enum HelperError {
    /// No helper (not installed, not running) or no right to its socket.
    Unreachable(io::Error),
    /// It closed without an answer: this user may not call it, or the request was malformed.
    NoAnswer,
    /// The answer is broken or too large.
    Malformed(String),
}

impl fmt::Display for HelperError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Unreachable(e) if e.kind() == io::ErrorKind::PermissionDenied => {
                write!(
                    f,
                    "no right to the root helper's socket {SOCKET} (only the user sermon may call it)"
                )
            }
            Self::Unreachable(e) => write!(
                f,
                "no root helper at {SOCKET}: {e} (package sermon-helper, or install.sh without --no-helper)"
            ),
            Self::NoAnswer => write!(
                f,
                "the root helper closed without an answer (it answers only the user sermon)"
            ),
            Self::Malformed(why) => write!(f, "broken answer from the root helper: {why}"),
        }
    }
}

impl std::error::Error for HelperError {}

/// Sends `request` to the helper at `socket` and returns its answer within `timeout` per read and
/// write.
///
/// # Errors
///
/// [`HelperError`] without an answer.
pub fn call(
    socket: &Path,
    request: &HelperRequest,
    timeout: Duration,
) -> Result<HelperResponse, HelperError> {
    let mut stream = UnixStream::connect(socket).map_err(HelperError::Unreachable)?;
    let broken = |e: io::Error| match e.kind() {
        io::ErrorKind::UnexpectedEof
        | io::ErrorKind::BrokenPipe
        | io::ErrorKind::ConnectionReset => HelperError::NoAnswer,
        _ => HelperError::Malformed(e.to_string()),
    };
    stream
        .set_read_timeout(Some(timeout))
        .and_then(|()| stream.set_write_timeout(Some(timeout)))
        .map_err(broken)?;
    let body = request.encode_to_vec();
    let len = u32::try_from(body.len())
        .map_err(|_| HelperError::Malformed("request too large".into()))?;
    stream
        .write_all(&len.to_be_bytes())
        .and_then(|()| stream.write_all(&body))
        .map_err(broken)?;
    let mut len = [0u8; 4];
    stream.read_exact(&mut len).map_err(broken)?;
    let len = usize::try_from(u32::from_be_bytes(len)).unwrap_or(usize::MAX);
    if len > MAX_RESPONSE {
        return Err(HelperError::Malformed(format!("answer of {len} bytes")));
    }
    let mut body = vec![0; len];
    stream.read_exact(&mut body).map_err(broken)?;
    HelperResponse::decode(body.as_slice()).map_err(|e| HelperError::Malformed(e.to_string()))
}

/// What `sermon-agent helper status` prints: version, distribution and what the host allows.
///
/// # Errors
///
/// Without an answer, or with a status other than OK.
pub fn status(socket: &Path) -> anyhow::Result<String> {
    let request = HelperRequest {
        operation: Some(Operation::GetStatus(GetStatus {})),
    };
    let response = call(socket, &request, Duration::from_secs(10))?;
    if response.status() != Status::Ok {
        anyhow::bail!(
            "the root helper answered {}: {}",
            response.status().as_str_name(),
            response.reason
        );
    }
    let Some(Answer::StatusInfo(info)) = response.result else {
        anyhow::bail!("the root helper sent no status");
    };
    let distribution = match info.distribution() {
        Distribution::Apt => "apt",
        Distribution::Dnf => "dnf",
        Distribution::Apk => "apk",
        Distribution::Unspecified => "unsupported",
    };
    let running = if info.running_run_id.is_empty() {
        "-"
    } else {
        &info.running_run_id
    };
    Ok(format!(
        "sermon-helper {} at {}\npackage manager: {distribution}\nallow_remote_updates = {}\nallow_remote_reboot = {}\nallow_self_update = {}\nrunning: {running}\n",
        response.helper_version,
        socket.display(),
        info.allow_remote_updates,
        info.allow_remote_reboot,
        info.allow_self_update,
    ))
}

#[cfg(test)]
mod tests {
    use std::os::unix::net::UnixListener;
    use std::thread;

    use sermon_helper_proto::v1::StatusInfo;

    use super::*;

    /// A helper that answers one connection with `answer` (raw bytes after reading the request).
    fn fake(
        answer: Vec<u8>,
    ) -> (
        tempfile::TempDir,
        std::path::PathBuf,
        thread::JoinHandle<Vec<u8>>,
    ) {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("helper.sock");
        let listener = UnixListener::bind(&path).unwrap();
        let server = thread::spawn(move || {
            let (mut s, _) = listener.accept().unwrap();
            let mut len = [0u8; 4];
            s.read_exact(&mut len).unwrap();
            let mut body = vec![0; usize::try_from(u32::from_be_bytes(len)).unwrap()];
            s.read_exact(&mut body).unwrap();
            let _ = s.write_all(&answer);
            body
        });
        (dir, path, server)
    }

    fn framed(response: &HelperResponse) -> Vec<u8> {
        let body = response.encode_to_vec();
        [
            u32::try_from(body.len()).unwrap().to_be_bytes().to_vec(),
            body,
        ]
        .concat()
    }

    #[test]
    fn asks_for_the_status_and_prints_it() {
        let response = HelperResponse {
            status: Status::Ok.into(),
            helper_version: "0.4.0".into(),
            result: Some(Answer::StatusInfo(StatusInfo {
                distribution: Distribution::Apt.into(),
                allow_remote_updates: true,
                allow_self_update: true,
                ..StatusInfo::default()
            })),
            ..HelperResponse::default()
        };
        let (_dir, path, server) = fake(framed(&response));
        let printed = status(&path).unwrap();
        assert!(printed.starts_with("sermon-helper 0.4.0 at "), "{printed}");
        assert!(
            printed.contains(
                "package manager: apt\nallow_remote_updates = true\nallow_remote_reboot = false\n"
            ),
            "{printed}"
        );
        let request = HelperRequest::decode(server.join().unwrap().as_slice()).unwrap();
        assert!(matches!(request.operation, Some(Operation::GetStatus(_))));
    }

    #[test]
    fn says_why_there_is_no_answer() {
        let dir = tempfile::tempdir().unwrap();
        let missing = call(
            &dir.path().join("none.sock"),
            &HelperRequest::default(),
            Duration::from_secs(1),
        );
        assert!(matches!(missing, Err(HelperError::Unreachable(_))));
        assert!(missing.unwrap_err().to_string().contains("sermon-helper"));
        // Closed without an answer: the helper refused this user.
        let (_dir, path, _server) = fake(Vec::new());
        assert!(matches!(
            call(&path, &HelperRequest::default(), Duration::from_secs(1)),
            Err(HelperError::NoAnswer)
        ));
        // Too large, broken.
        let (_dir, path, _server) = fake(u32::MAX.to_be_bytes().to_vec());
        assert!(matches!(
            call(&path, &HelperRequest::default(), Duration::from_secs(1)),
            Err(HelperError::Malformed(_))
        ));
        let (_dir, path, _server) = fake(vec![0, 0, 0, 2, 0xff, 0xff]);
        assert!(matches!(
            call(&path, &HelperRequest::default(), Duration::from_secs(1)),
            Err(HelperError::Malformed(_))
        ));
        // A refusal is an error with its reason.
        let refused = HelperResponse {
            status: Status::Refused.into(),
            reason: "no".into(),
            ..HelperResponse::default()
        };
        let (_dir, path, _server) = fake(framed(&refused));
        assert!(
            status(&path)
                .unwrap_err()
                .to_string()
                .contains("STATUS_REFUSED: no")
        );
    }
}
