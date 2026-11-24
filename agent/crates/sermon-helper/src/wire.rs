//! The socket's frames (ADR 0035): a length of 4 bytes (big endian), then the message. A request
//! must arrive completely within [`REQUEST_TIME`]; nothing larger than the bounds is read.

use std::io::{self, Read, Write};
use std::os::unix::net::UnixStream;
use std::time::{Duration, Instant};

/// Largest request.
pub const MAX_REQUEST: usize = 64 * 1024;
/// Largest answer.
pub const MAX_RESPONSE: usize = 8 * 1024 * 1024;
/// Time for a whole request.
pub const REQUEST_TIME: Duration = Duration::from_secs(5);
/// Time to write an answer.
pub const RESPONSE_TIME: Duration = Duration::from_secs(30);

/// Why no request was read.
#[derive(Debug)]
pub enum ReadError {
    /// The length exceeds [`MAX_REQUEST`]; the body is not read.
    TooLarge(usize),
    /// Not complete within [`REQUEST_TIME`], or the connection broke.
    Io(io::Error),
}

/// Reads one frame of at most `max` bytes, all of it before `deadline`.
///
/// # Errors
///
/// [`ReadError`] for a frame too large, too slow or broken off.
pub fn read_frame(
    stream: &mut UnixStream,
    max: usize,
    deadline: Instant,
) -> Result<Vec<u8>, ReadError> {
    let mut len = [0u8; 4];
    read_until(stream, &mut len, deadline).map_err(ReadError::Io)?;
    let len = usize::try_from(u32::from_be_bytes(len)).unwrap_or(usize::MAX);
    if len > max {
        return Err(ReadError::TooLarge(len));
    }
    let mut body = vec![0; len];
    read_until(stream, &mut body, deadline).map_err(ReadError::Io)?;
    Ok(body)
}

/// Fills `buf`, each read bounded by what is left until `deadline`: a slow writer can't hold the
/// helper longer than that.
fn read_until(stream: &mut UnixStream, buf: &mut [u8], deadline: Instant) -> io::Result<()> {
    let mut filled = 0;
    while filled < buf.len() {
        let left = deadline.saturating_duration_since(Instant::now());
        if left.is_zero() {
            return Err(io::ErrorKind::TimedOut.into());
        }
        if let Err(e) = stream.set_read_timeout(Some(left)) {
            // macOS (the developers' machines) refuses it once the peer has closed; the read then
            // doesn't block. Elsewhere every error counts.
            if !(cfg!(target_os = "macos") && e.kind() == io::ErrorKind::InvalidInput) {
                return Err(e);
            }
        }
        match stream.read(&mut buf[filled..]) {
            Ok(0) => return Err(io::ErrorKind::UnexpectedEof.into()),
            Ok(n) => filled += n,
            Err(e) if e.kind() == io::ErrorKind::Interrupted => {}
            Err(e) => return Err(e),
        }
    }
    Ok(())
}

/// Writes one frame.
///
/// # Errors
///
/// If the frame is too large for its length or the peer doesn't take it.
pub fn write_frame(stream: &mut UnixStream, body: &[u8]) -> io::Result<()> {
    let len =
        u32::try_from(body.len()).map_err(|_| io::Error::from(io::ErrorKind::InvalidInput))?;
    stream.set_write_timeout(Some(RESPONSE_TIME))?;
    stream.write_all(&len.to_be_bytes())?;
    stream.write_all(body)?;
    stream.flush()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn soon() -> Instant {
        Instant::now() + Duration::from_millis(300)
    }

    #[test]
    fn reads_and_writes_frames() {
        let (mut a, mut b) = UnixStream::pair().unwrap();
        write_frame(&mut a, b"hello").unwrap();
        write_frame(&mut a, b"").unwrap();
        assert_eq!(read_frame(&mut b, 16, soon()).unwrap(), b"hello");
        assert_eq!(read_frame(&mut b, 16, soon()).unwrap(), b"");
    }

    #[test]
    fn a_large_length_is_refused_before_reading_the_body() {
        let (mut a, mut b) = UnixStream::pair().unwrap();
        a.write_all(&u32::MAX.to_be_bytes()).unwrap();
        assert!(
            matches!(read_frame(&mut b, MAX_REQUEST, soon()), Err(ReadError::TooLarge(n)) if u32::try_from(n) == Ok(u32::MAX))
        );
        let (mut a, mut b) = UnixStream::pair().unwrap();
        write_frame(&mut a, &[0; 17]).unwrap();
        assert!(matches!(
            read_frame(&mut b, 16, soon()),
            Err(ReadError::TooLarge(17))
        ));
    }

    // A client that sends part of a request and then waits still ends at the deadline.
    #[test]
    fn a_slow_or_short_request_ends_at_the_deadline() {
        let (mut a, mut b) = UnixStream::pair().unwrap();
        a.write_all(&4u32.to_be_bytes()).unwrap();
        a.write_all(b"x").unwrap();
        let started = Instant::now();
        assert!(
            matches!(read_frame(&mut b, 16, soon()), Err(ReadError::Io(e)) if matches!(e.kind(), io::ErrorKind::TimedOut | io::ErrorKind::WouldBlock))
        );
        assert!(started.elapsed() < Duration::from_millis(600));
        drop(a);
        // The peer goes away mid-frame.
        let (mut a, mut b) = UnixStream::pair().unwrap();
        a.write_all(&[0, 0]).unwrap();
        drop(a);
        assert!(
            matches!(read_frame(&mut b, 16, soon()), Err(ReadError::Io(e)) if e.kind() == io::ErrorKind::UnexpectedEof)
        );
    }
}
