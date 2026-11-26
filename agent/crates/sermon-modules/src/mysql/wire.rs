//! The protocol of MySQL and MariaDB as far as monitoring needs it: the handshake (v10), the
//! logins `mysql_native_password` and `caching_sha2_password` (fast path; the full login sends
//! the password only over TLS or a Unix socket – the RSA way would need another crypto crate), an
//! auth switch between them, `COM_QUERY` with text results. Bounded: a packet at most
//! [`MAX_PACKET`] (no multi-packet payloads), an answer at most [`MAX_ANSWER`] bytes and
//! [`MAX_ROWS`] rows. The decoding is sync and pure ([`Query::feed`], [`Handshake::parse`]),
//! so it is fuzzed without a server.

use ring::digest;
use tokio::io::{AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt};

/// Largest packet taken.
pub const MAX_PACKET: usize = 1 << 20;
/// Largest answer to one query.
pub const MAX_ANSWER: usize = 1 << 20;
/// Most rows of one answer (`SHOW GLOBAL STATUS` has about 500).
pub const MAX_ROWS: usize = 4096;
const MAX_COLUMNS: usize = 128;

const LONG_PASSWORD: u32 = 1;
const CONNECT_WITH_DB: u32 = 8;
const PROTOCOL_41: u32 = 0x200;
const SSL: u32 = 0x800;
const TRANSACTIONS: u32 = 0x2000;
const SECURE_CONNECTION: u32 = 0x8000;
const PLUGIN_AUTH: u32 = 0x8_0000;
const PLUGIN_AUTH_LENENC: u32 = 0x20_0000;
/// What this client speaks, as far as the server does too.
const CAPABILITIES: u32 = LONG_PASSWORD
    | CONNECT_WITH_DB
    | PROTOCOL_41
    | TRANSACTIONS
    | SECURE_CONNECTION
    | PLUGIN_AUTH
    | PLUGIN_AUTH_LENENC;
/// `utf8mb4_general_ci`, known by every supported version.
const CHARSET: u8 = 45;

/// Why a session failed.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// The server refused: its error number and message (e.g. 1045 for a wrong password).
    #[error("{message} (error {code})")]
    Server { code: u16, message: String },
    /// The server answered something this client doesn't take.
    #[error("{0}")]
    Protocol(String),
    /// The connection failed.
    #[error("{0}")]
    Io(#[from] std::io::Error),
}

fn protocol(why: impl Into<String>) -> Error {
    Error::Protocol(why.into())
}

/// A bounds-checked reader of a packet.
struct Body<'a>(&'a [u8]);

impl<'a> Body<'a> {
    fn take(&mut self, n: usize) -> Result<&'a [u8], Error> {
        if self.0.len() < n {
            return Err(protocol("a packet ends early"));
        }
        let (head, rest) = self.0.split_at(n);
        self.0 = rest;
        Ok(head)
    }
    fn u8(&mut self) -> Result<u8, Error> {
        Ok(self.take(1)?[0])
    }
    fn u16(&mut self) -> Result<u16, Error> {
        let b = self.take(2)?;
        Ok(u16::from_le_bytes([b[0], b[1]]))
    }
    fn cstr(&mut self) -> Result<&'a [u8], Error> {
        let end = self
            .0
            .iter()
            .position(|b| *b == 0)
            .ok_or_else(|| protocol("a string without its end"))?;
        let s = &self.0[..end];
        self.0 = &self.0[end + 1..];
        Ok(s)
    }
    /// A length-encoded integer; `None` for the NULL marker (0xfb).
    fn lenenc(&mut self) -> Result<Option<u64>, Error> {
        Ok(Some(match self.u8()? {
            0xfb => return Ok(None),
            0xfc => u64::from(self.u16()?),
            0xfd => {
                let b = self.take(3)?;
                u64::from(u32::from_le_bytes([b[0], b[1], b[2], 0]))
            }
            0xfe => {
                let b = self.take(8)?;
                u64::from_le_bytes([b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7]])
            }
            0xff => return Err(protocol("0xff is no length")),
            n => u64::from(n),
        }))
    }
    /// A length-encoded string; `None` for NULL.
    fn lenenc_str(&mut self) -> Result<Option<&'a [u8]>, Error> {
        match self.lenenc()? {
            None => Ok(None),
            Some(n) => {
                let n = usize::try_from(n).map_err(|_| protocol("a string too long"))?;
                self.take(n).map(Some)
            }
        }
    }
}

/// The server's greeting.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Handshake {
    pub version: String,
    pub capabilities: u32,
    /// The 20 bytes the password is scrambled with.
    pub scramble: Vec<u8>,
    pub plugin: String,
}

impl Handshake {
    /// Whether the server offers TLS.
    #[must_use]
    pub fn tls(&self) -> bool {
        self.capabilities & SSL != 0
    }

    /// Parses the handshake packet (protocol 10); an ERR packet is the server's refusal (too
    /// many connections, host blocked).
    ///
    /// # Errors
    ///
    /// [`Error::Server`] for an ERR packet, [`Error::Protocol`] for anything but protocol 10.
    pub fn parse(p: &[u8]) -> Result<Self, Error> {
        if p.first() == Some(&0xff) {
            return Err(err_packet(p));
        }
        let mut b = Body(p);
        if b.u8()? != 10 {
            return Err(protocol("not protocol 10 (MySQL 5.x or later)"));
        }
        let version = String::from_utf8_lossy(b.cstr()?)
            .chars()
            .take(64)
            .collect();
        b.take(4)?;
        let mut scramble = b.take(8)?.to_vec();
        b.take(1)?;
        let lower = u32::from(b.u16()?);
        let (mut upper, mut data_len, mut plugin) = (0, 0usize, String::new());
        if !b.0.is_empty() {
            b.take(3)?;
            upper = u32::from(b.u16()?);
            data_len = usize::from(b.u8()?);
            b.take(10)?;
        }
        let capabilities = lower | upper << 16;
        if capabilities & SECURE_CONNECTION != 0 {
            let n = data_len.saturating_sub(8).max(13);
            let rest = b.take(n.min(b.0.len()))?;
            scramble.extend_from_slice(rest.strip_suffix(&[0]).unwrap_or(rest));
        }
        if capabilities & PLUGIN_AUTH != 0 {
            plugin = String::from_utf8_lossy(b.cstr().unwrap_or(b.0)).into_owned();
        }
        if capabilities & PROTOCOL_41 == 0 {
            return Err(protocol("the server speaks no protocol 4.1"));
        }
        // Every server of protocol 4.1 scrambles with 20 bytes.
        if scramble.len() != 20 {
            return Err(protocol(format!("a scramble of {} bytes", scramble.len())));
        }
        Ok(Self {
            version,
            capabilities,
            scramble,
            plugin,
        })
    }
}

/// The error of an ERR packet.
#[must_use]
pub fn err_packet(p: &[u8]) -> Error {
    let mut b = Body(p.get(1..).unwrap_or_default());
    let code = b.u16().unwrap_or(0);
    // `#` and the SQLSTATE, from protocol 4.1 on.
    if b.0.first() == Some(&b'#') {
        let _ = b.take(6);
    }
    let message = String::from_utf8_lossy(b.0).chars().take(256).collect();
    Error::Server { code, message }
}

/// One result set: column names and rows as text (`None` for NULL).
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ResultSet {
    pub columns: Vec<String>,
    pub rows: Vec<Vec<Option<String>>>,
}

/// Where an answer to `COM_QUERY` is.
#[derive(Debug, Default)]
enum Phase {
    #[default]
    Start,
    Columns(usize),
    Rows,
}

/// The answer to one query, packet by packet (protocol 4.1 with EOF packets).
#[derive(Debug, Default)]
pub struct Query {
    phase: Phase,
    set: ResultSet,
    bytes: usize,
}

fn is_eof(p: &[u8]) -> bool {
    p.first() == Some(&0xfe) && p.len() < 9
}

impl Query {
    /// Takes the next packet; `Some` once the answer is complete.
    ///
    /// # Errors
    ///
    /// [`Error::Server`] for an ERR packet, [`Error::Protocol`] for a broken packet or one beyond
    /// the bounds.
    pub fn feed(&mut self, p: &[u8]) -> Result<Option<ResultSet>, Error> {
        self.bytes = self.bytes.saturating_add(p.len() + 4);
        if self.bytes > MAX_ANSWER {
            return Err(protocol(format!(
                "an answer of more than {MAX_ANSWER} bytes"
            )));
        }
        if p.first() == Some(&0xff) {
            return Err(err_packet(p));
        }
        match self.phase {
            Phase::Start => {
                if p.first() == Some(&0) {
                    // OK: a statement without a result.
                    return Ok(Some(ResultSet::default()));
                }
                let n = Body(p)
                    .lenenc()?
                    .and_then(|n| usize::try_from(n).ok())
                    .filter(|n| (1..=MAX_COLUMNS).contains(n))
                    .ok_or_else(|| protocol("a column count out of bounds"))?;
                self.phase = Phase::Columns(n);
            }
            Phase::Columns(n) if self.set.columns.len() == n => {
                if !is_eof(p) {
                    return Err(protocol("no EOF after the columns"));
                }
                self.phase = Phase::Rows;
            }
            Phase::Columns(_) => {
                let mut b = Body(p);
                for _ in 0..4 {
                    b.lenenc_str()?;
                }
                let name = b.lenenc_str()?.unwrap_or_default();
                self.set
                    .columns
                    .push(String::from_utf8_lossy(name).into_owned());
            }
            Phase::Rows if is_eof(p) => return Ok(Some(std::mem::take(&mut self.set))),
            Phase::Rows => {
                if self.set.rows.len() >= MAX_ROWS {
                    return Err(protocol(format!("more than {MAX_ROWS} rows")));
                }
                let mut b = Body(p);
                let mut row = Vec::with_capacity(self.set.columns.len());
                for _ in 0..self.set.columns.len() {
                    row.push(
                        b.lenenc_str()?
                            .map(|v| String::from_utf8_lossy(v).into_owned()),
                    );
                }
                if !b.0.is_empty() {
                    return Err(protocol("a row with more values than columns"));
                }
                self.set.rows.push(row);
            }
        }
        Ok(None)
    }
}

/// Splits `bytes` into packet payloads (3 bytes length, 1 byte sequence); the rest that is no
/// whole packet stays.
///
/// # Errors
///
/// [`Error::Protocol`] for a packet beyond [`MAX_PACKET`] (or a multi-packet payload).
pub fn split(mut bytes: &[u8]) -> Result<(Vec<Vec<u8>>, &[u8]), Error> {
    let mut out = Vec::new();
    while bytes.len() >= 4 {
        let len = length(&bytes[..3])?;
        if bytes.len() < 4 + len {
            break;
        }
        out.push(bytes[4..4 + len].to_vec());
        bytes = &bytes[4 + len..];
    }
    Ok((out, bytes))
}

fn length(b: &[u8]) -> Result<usize, Error> {
    let len = usize::try_from(u32::from_le_bytes([b[0], b[1], b[2], 0]))
        .map_err(|_| protocol("a packet too long"))?;
    if len > MAX_PACKET {
        return Err(protocol(format!("a packet of {len} bytes")));
    }
    Ok(len)
}

/// SHA-1 scramble of `mysql_native_password`: SHA1(pw) XOR SHA1(scramble + SHA1(SHA1(pw))).
#[must_use]
pub fn native_password(password: &str, scramble: &[u8]) -> Vec<u8> {
    if password.is_empty() {
        return Vec::new();
    }
    let sha1 = |d: &[u8]| digest::digest(&digest::SHA1_FOR_LEGACY_USE_ONLY, d);
    let stage1 = sha1(password.as_bytes());
    let stage2 = sha1(stage1.as_ref());
    let mix = sha1(&[&scramble[..scramble.len().min(20)], stage2.as_ref()].concat());
    stage1
        .as_ref()
        .iter()
        .zip(mix.as_ref())
        .map(|(a, b)| a ^ b)
        .collect()
}

/// SHA-256 scramble of `caching_sha2_password`: SHA256(pw) XOR SHA256(SHA256(SHA256(pw)) + scramble).
#[must_use]
pub fn sha2_password(password: &str, scramble: &[u8]) -> Vec<u8> {
    if password.is_empty() {
        return Vec::new();
    }
    let sha = |d: &[u8]| digest::digest(&digest::SHA256, d);
    let stage1 = sha(password.as_bytes());
    let stage2 = sha(stage1.as_ref());
    let mix = sha(&[stage2.as_ref(), &scramble[..scramble.len().min(20)]].concat());
    stage1
        .as_ref()
        .iter()
        .zip(mix.as_ref())
        .map(|(a, b)| a ^ b)
        .collect()
}

/// A connection with the packets' sequence numbers.
#[derive(Debug)]
pub struct Packets<S> {
    io: S,
    seq: u8,
}

impl<S: AsyncRead + AsyncWrite + Unpin> Packets<S> {
    /// Packets over `io`.
    pub fn new(io: S) -> Self {
        Self { io, seq: 0 }
    }

    /// The connection, for TLS.
    pub fn into_inner(self) -> (S, u8) {
        (self.io, self.seq)
    }

    /// Packets over `io` that continue at `seq` (after TLS).
    pub fn resume(io: S, seq: u8) -> Self {
        Self { io, seq }
    }

    /// The next packet's payload.
    ///
    /// # Errors
    ///
    /// [`Error`] if the connection fails or the packet is too large.
    pub async fn read(&mut self) -> Result<Vec<u8>, Error> {
        let mut head = [0u8; 4];
        self.io.read_exact(&mut head).await?;
        let len = length(&head[..3])?;
        self.seq = head[3].wrapping_add(1);
        let mut body = vec![0; len];
        self.io.read_exact(&mut body).await?;
        Ok(body)
    }

    /// Sends `payload` as the next packet.
    ///
    /// # Errors
    ///
    /// [`Error`] if the connection fails or the payload is too large.
    pub async fn write(&mut self, payload: &[u8]) -> Result<(), Error> {
        let len = u32::try_from(payload.len())
            .ok()
            .filter(|l| usize::try_from(*l).is_ok_and(|l| l <= MAX_PACKET))
            .ok_or_else(|| protocol("a packet too large"))?;
        let mut out = len.to_le_bytes()[..3].to_vec();
        out.push(self.seq);
        out.extend_from_slice(payload);
        self.seq = self.seq.wrapping_add(1);
        self.io.write_all(&out).await?;
        Ok(())
    }

    /// Runs `sql` (one statement) and returns its rows.
    ///
    /// # Errors
    ///
    /// [`Error::Server`] if it failed (e.g. 1227 without a right), [`Error`] else.
    pub async fn query(&mut self, sql: &str) -> Result<ResultSet, Error> {
        self.seq = 0;
        self.write(&[&[0x03], sql.as_bytes()].concat()).await?;
        let mut q = Query::default();
        loop {
            let p = self.read().await?;
            if let Some(set) = q.feed(&p)? {
                return Ok(set);
            }
        }
    }

    /// Ends the session (`COM_QUIT`).
    pub async fn close(mut self) {
        self.seq = 0;
        let _ = self.write(&[0x01]).await;
        let _ = self.io.shutdown().await;
    }
}

/// Who logs in where.
#[derive(Clone, Copy)]
pub struct Login<'a> {
    pub user: &'a str,
    pub password: &'a str,
    pub database: &'a str,
    /// Over TLS or a Unix socket: the full login of `caching_sha2_password` may send the
    /// password itself.
    pub secure: bool,
}

impl std::fmt::Debug for Login<'_> {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Login")
            .field("user", &self.user)
            .field("secure", &self.secure)
            .finish_non_exhaustive()
    }
}

/// The SSL request: the client's capabilities with `SSL`, before TLS.
#[must_use]
pub fn ssl_request(server: u32) -> Vec<u8> {
    let caps = (CAPABILITIES | SSL) & (server | SSL);
    [
        &caps.to_le_bytes()[..],
        &(1u32 << 24).to_le_bytes(),
        &[CHARSET],
        &[0; 23],
    ]
    .concat()
}

/// The handshake response with the login, for a server with `server` capabilities.
///
/// # Errors
///
/// [`Error::Protocol`] for a NUL in user or database, or an unknown login plugin.
pub fn handshake_response(h: &Handshake, login: &Login<'_>, tls: bool) -> Result<Vec<u8>, Error> {
    if login.user.contains('\0') || login.database.contains('\0') {
        return Err(protocol("a NUL in user or database"));
    }
    let caps = (CAPABILITIES | if tls { SSL } else { 0 }) & h.capabilities;
    let (plugin, auth) = scramble_for(&h.plugin, login.password, &h.scramble)?;
    let mut out = [
        &caps.to_le_bytes()[..],
        &(1u32 << 24).to_le_bytes(),
        &[CHARSET],
        &[0; 23],
    ]
    .concat();
    out.extend_from_slice(login.user.as_bytes());
    out.push(0);
    // At most 32 bytes: as a length-encoded string and with SECURE_CONNECTION alike one byte of
    // length first.
    out.push(u8::try_from(auth.len()).unwrap_or(0));
    out.extend_from_slice(&auth);
    if caps & CONNECT_WITH_DB != 0 {
        out.extend_from_slice(login.database.as_bytes());
        out.push(0);
    }
    if caps & PLUGIN_AUTH != 0 {
        out.extend_from_slice(plugin.as_bytes());
        out.push(0);
    }
    Ok(out)
}

/// The plugin and scrambled password for the server's plugin `name`.
fn scramble_for(
    name: &str,
    password: &str,
    scramble: &[u8],
) -> Result<(&'static str, Vec<u8>), Error> {
    match name {
        "caching_sha2_password" => Ok(("caching_sha2_password", sha2_password(password, scramble))),
        "mysql_native_password" | "" => {
            Ok(("mysql_native_password", native_password(password, scramble)))
        }
        other => Err(protocol(format!(
            "the login plugin {other} is not supported"
        ))),
    }
}

/// Logs in on `packets` after the handshake response was sent: auth switches, the fast and the
/// full path of `caching_sha2_password`.
///
/// # Errors
///
/// [`Error::Server`] for a refusal (1045: wrong password), [`Error::Protocol`] if the login
/// needs what this client doesn't do (RSA without TLS).
pub async fn finish_login<S: AsyncRead + AsyncWrite + Unpin>(
    packets: &mut Packets<S>,
    login: &Login<'_>,
) -> Result<(), Error> {
    for _ in 0..4 {
        let p = packets.read().await?;
        match p.first() {
            Some(0x00) => return Ok(()),
            Some(0xff) => return Err(err_packet(&p)),
            // Auth switch: another plugin and scramble.
            Some(0xfe) => {
                let mut b = Body(&p[1..]);
                let name = String::from_utf8_lossy(b.cstr()?).into_owned();
                let data = b.0.strip_suffix(&[0]).unwrap_or(b.0);
                let (_, auth) = scramble_for(&name, login.password, data)?;
                packets.write(&auth).await?;
            }
            // caching_sha2_password: 3 = fast login done (OK follows), 4 = full login.
            Some(0x01) => match p.get(1) {
                Some(3) => {}
                Some(4) if login.secure => {
                    packets
                        .write(&[login.password.as_bytes(), b"\0"].concat())
                        .await?;
                }
                Some(4) => {
                    return Err(protocol(
                        "caching_sha2_password asks for a full login: the password is wrong, or the server has no cached login of the user yet (first login, restart); a full login sends the password and needs TLS or the socket",
                    ));
                }
                _ => return Err(protocol("an unknown login step")),
            },
            _ => return Err(protocol("an unexpected answer to the login")),
        }
    }
    Err(protocol("the login doesn't end"))
}

#[cfg(test)]
mod tests;
