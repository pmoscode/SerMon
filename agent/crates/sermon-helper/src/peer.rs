//! Who calls (ADR 0035): only the user `sermon`, by the kernel's credentials of the socket
//! (`SO_PEERCRED`). Its IDs come from `/etc/passwd`, never from the environment.

use std::io;
use std::os::unix::net::UnixStream;

/// The user the agent runs as.
pub const USER: &str = "sermon";

/// User and group ID of `user` in the text of `/etc/passwd`.
#[must_use]
pub fn ids_of(passwd: &str, user: &str) -> Option<(u32, u32)> {
    passwd.lines().find_map(|line| {
        let mut fields = line.split(':');
        if fields.next()? != user {
            return None;
        }
        let _password = fields.next()?;
        Some((fields.next()?.parse().ok()?, fields.next()?.parse().ok()?))
    })
}

/// The user ID of the process at the other end, from the kernel.
///
/// # Errors
///
/// If the kernel gives no credentials.
#[cfg(target_os = "linux")]
pub fn peer_uid(stream: &UnixStream) -> io::Result<u32> {
    let cred = rustix::net::sockopt::socket_peercred(stream)?;
    Ok(cred.uid.as_raw())
}

/// Without `SO_PEERCRED` nobody is let in.
///
/// # Errors
///
/// Always: unsupported.
#[cfg(not(target_os = "linux"))]
pub fn peer_uid(_: &UnixStream) -> io::Result<u32> {
    Err(io::ErrorKind::Unsupported.into())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn finds_the_user_and_nothing_else() {
        let passwd = "root:x:0:0:root:/root:/bin/sh\n# comment\nsermonx:x:5:5::/:/sbin/nologin\n\
                      sermon:x:997:996:SerMon agent:/var/lib/sermon-agent:/usr/sbin/nologin\n";
        assert_eq!(ids_of(passwd, USER), Some((997, 996)));
        assert_eq!(ids_of(passwd, "nobody"), None);
        assert_eq!(ids_of("sermon:x:abc:1::/:/bin/sh", USER), None);
        assert_eq!(ids_of("sermon:x", USER), None);
        // Only the first entry counts, as for the C library.
        assert_eq!(
            ids_of("sermon:x:1:1::/:\nsermon:x:0:0::/:", USER),
            Some((1, 1))
        );
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn reads_the_peers_uid() {
        let (a, _b) = UnixStream::pair().unwrap();
        assert_eq!(peer_uid(&a).unwrap(), rustix::process::getuid().as_raw());
    }
}
