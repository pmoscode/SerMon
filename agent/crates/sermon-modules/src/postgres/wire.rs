//! The protocol of PostgreSQL (3.0) as far as monitoring needs it: the startup, the methods
//! trust, password, md5 and SCRAM-SHA-256 (the server's signature verified too), the simple
//! query protocol with results as text. Bounded: a message at most [`MAX_MESSAGE`], a query's
//! answer at most [`MAX_ANSWER`] bytes and [`MAX_ROWS`] rows. The decoding is sync and pure
//! ([`Query::feed`]), so it is fuzzed without a server.

use std::num::NonZeroU32;

use base64::Engine;
use base64::engine::general_purpose::STANDARD as B64;
use ring::{digest, hmac, pbkdf2};
use tokio::io::{AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt};

/// Largest message taken.
pub const MAX_MESSAGE: usize = 1 << 20;
/// Largest answer to one query, all messages together.
pub const MAX_ANSWER: usize = 1 << 20;
/// Most rows of all result sets of one query.
pub const MAX_ROWS: usize = 4096;
/// Most columns of a result set.
const MAX_COLUMNS: usize = 64;
/// Most PBKDF2 iterations of SCRAM a server may ask for (PostgreSQL uses 4 096).
const MAX_ITERATIONS: u32 = 100_000;

/// Why a session failed.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// The server refused: its SQLSTATE and message (e.g. 28P01 for a wrong password).
    #[error("{message} (SQLSTATE {code})")]
    Server { code: String, message: String },
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

/// A message of the server: its type and body.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Message {
    pub tag: u8,
    pub body: Vec<u8>,
}

/// One result set of a query: column names and rows as text (`None` for NULL).
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ResultSet {
    pub columns: Vec<String>,
    pub rows: Vec<Vec<Option<String>>>,
}

/// A bounds-checked reader of a message body.
struct Body<'a>(&'a [u8]);

impl<'a> Body<'a> {
    fn take(&mut self, n: usize) -> Result<&'a [u8], Error> {
        if self.0.len() < n {
            return Err(protocol("a message ends early"));
        }
        let (head, rest) = self.0.split_at(n);
        self.0 = rest;
        Ok(head)
    }
    fn i16(&mut self) -> Result<i16, Error> {
        let b = self.take(2)?;
        Ok(i16::from_be_bytes([b[0], b[1]]))
    }
    fn i32(&mut self) -> Result<i32, Error> {
        let b = self.take(4)?;
        Ok(i32::from_be_bytes([b[0], b[1], b[2], b[3]]))
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
}

/// The fields of an `ErrorResponse`: SQLSTATE (`C`) and message (`M`).
#[must_use]
pub fn error_fields(body: &[u8]) -> (String, String) {
    let (mut code, mut message) = (String::new(), String::new());
    let mut b = Body(body);
    while let Ok([kind]) = b.take(1) {
        let Ok(value) = b.cstr() else { break };
        let value = String::from_utf8_lossy(value);
        match kind {
            b'C' => code = value.chars().take(5).collect(),
            b'M' => message = value.chars().take(256).collect(),
            0 => break,
            _ => {}
        }
    }
    (code, message)
}

/// The column names of a `RowDescription`.
///
/// # Errors
///
/// [`Error::Protocol`] for a broken one.
pub fn row_description(body: &[u8]) -> Result<Vec<String>, Error> {
    let mut b = Body(body);
    let n = usize::try_from(b.i16()?).map_err(|_| protocol("a negative column count"))?;
    if n > MAX_COLUMNS {
        return Err(protocol(format!("{n} columns")));
    }
    let mut columns = Vec::with_capacity(n);
    for _ in 0..n {
        columns.push(String::from_utf8_lossy(b.cstr()?).into_owned());
        b.take(18)?;
    }
    Ok(columns)
}

/// The values of a `DataRow` of `columns` columns.
///
/// # Errors
///
/// [`Error::Protocol`] for a broken one or another number of values.
pub fn data_row(body: &[u8], columns: usize) -> Result<Vec<Option<String>>, Error> {
    let mut b = Body(body);
    let n = usize::try_from(b.i16()?).map_err(|_| protocol("a negative value count"))?;
    if n != columns {
        return Err(protocol(format!("{n} values for {columns} columns")));
    }
    let mut row = Vec::with_capacity(n);
    for _ in 0..n {
        let len = b.i32()?;
        row.push(if len < 0 {
            None
        } else {
            let len = usize::try_from(len).map_err(|_| protocol("a value too long"))?;
            Some(String::from_utf8_lossy(b.take(len)?).into_owned())
        });
    }
    Ok(row)
}

/// The answer to one simple query, message by message.
#[derive(Debug, Default)]
pub struct Query {
    sets: Vec<ResultSet>,
    current: Option<ResultSet>,
    error: Option<Error>,
    bytes: usize,
    rows: usize,
}

impl Query {
    /// Takes the next message; `Some` once the server is ready again (`ReadyForQuery`).
    ///
    /// # Errors
    ///
    /// [`Error::Protocol`] for a broken message or one beyond the bounds.
    pub fn feed(&mut self, m: &Message) -> Result<Option<Result<Vec<ResultSet>, Error>>, Error> {
        self.bytes = self.bytes.saturating_add(m.body.len() + 5);
        if self.bytes > MAX_ANSWER {
            return Err(protocol(format!(
                "an answer of more than {MAX_ANSWER} bytes"
            )));
        }
        match m.tag {
            b'T' => {
                self.current = Some(ResultSet {
                    columns: row_description(&m.body)?,
                    rows: Vec::new(),
                });
            }
            b'D' => {
                let set = self
                    .current
                    .as_mut()
                    .ok_or_else(|| protocol("a row without its description"))?;
                self.rows += 1;
                if self.rows > MAX_ROWS {
                    return Err(protocol(format!("more than {MAX_ROWS} rows")));
                }
                set.rows.push(data_row(&m.body, set.columns.len())?);
            }
            b'C' | b'I' => self.sets.push(self.current.take().unwrap_or_default()),
            b'E' => {
                let (code, message) = error_fields(&m.body);
                self.current = None;
                self.error.get_or_insert(Error::Server { code, message });
            }
            // Notices, parameters, notifications: nothing for monitoring.
            b'N' | b'S' | b'A' => {}
            b'Z' => {
                return Ok(Some(match self.error.take() {
                    Some(e) => Err(e),
                    None => Ok(std::mem::take(&mut self.sets)),
                }));
            }
            other => {
                return Err(protocol(format!(
                    "unexpected message {:?}",
                    char::from(other)
                )));
            }
        }
        Ok(None)
    }
}

/// Splits `bytes` into messages (type, length, body) as the server sends them; the rest that is
/// no whole message stays.
///
/// # Errors
///
/// [`Error::Protocol`] for a length out of bounds.
pub fn split(mut bytes: &[u8]) -> Result<(Vec<Message>, &[u8]), Error> {
    let mut out = Vec::new();
    while bytes.len() >= 5 {
        let len = length(&bytes[1..5])?;
        if bytes.len() < 1 + len {
            break;
        }
        out.push(Message {
            tag: bytes[0],
            body: bytes[5..=len].to_vec(),
        });
        bytes = &bytes[1 + len..];
    }
    Ok((out, bytes))
}

/// The length field of a message: including itself, at least 4, at most [`MAX_MESSAGE`].
fn length(b: &[u8]) -> Result<usize, Error> {
    let len = usize::try_from(u32::from_be_bytes([b[0], b[1], b[2], b[3]]))
        .map_err(|_| protocol("a message too long"))?;
    if !(4..=MAX_MESSAGE).contains(&len) {
        return Err(protocol(format!("a message of {len} bytes")));
    }
    Ok(len)
}

async fn read<S: AsyncRead + Unpin>(io: &mut S) -> Result<Message, Error> {
    let mut head = [0u8; 5];
    io.read_exact(&mut head).await?;
    let len = length(&head[1..])?;
    let mut body = vec![0; len - 4];
    io.read_exact(&mut body).await?;
    Ok(Message { tag: head[0], body })
}

fn message(tag: u8, body: &[u8]) -> Vec<u8> {
    let len = u32::try_from(body.len() + 4).unwrap_or(u32::MAX);
    let mut out = Vec::with_capacity(body.len() + 5);
    out.push(tag);
    out.extend_from_slice(&len.to_be_bytes());
    out.extend_from_slice(body);
    out
}

/// Asks for TLS (`SSLRequest`); true if the server agrees. Exactly one byte is read: what follows
/// belongs to the handshake (CVE-2021-23222).
///
/// # Errors
///
/// [`Error`] if the connection fails or the answer is neither yes nor no.
pub async fn ssl_request<S: AsyncRead + AsyncWrite + Unpin>(io: &mut S) -> Result<bool, Error> {
    io.write_all(&[0, 0, 0, 8, 0x04, 0xd2, 0x16, 0x2f]).await?;
    let mut answer = [0u8; 1];
    io.read_exact(&mut answer).await?;
    match answer[0] {
        b'S' => Ok(true),
        b'N' => Ok(false),
        _ => Err(protocol("no answer to the TLS request")),
    }
}

/// Who connects where.
#[derive(Clone, Copy)]
pub struct Startup<'a> {
    pub user: &'a str,
    pub database: &'a str,
    pub password: Option<&'a str>,
    /// Command-line options of the session, e.g. `-c statement_timeout=3000`.
    pub options: &'a str,
    /// The weakest method of authentication taken (`auth` of the module).
    pub allow: Allow,
    /// Over the Unix socket or TLS with a verified certificate: only then may the password
    /// itself (method `password`) go out.
    pub secure: bool,
}

impl std::fmt::Debug for Startup<'_> {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Startup")
            .field("user", &self.user)
            .field("database", &self.database)
            .field("password", &self.password.map(|_| "…"))
            .field("allow", &self.allow)
            .field("secure", &self.secure)
            .finish_non_exhaustive()
    }
}

/// The weakest authentication the client takes (like libpq's `require_auth`). Whatever answers
/// on the module's address – also a local impostor on a free port – must not get the password
/// or a hash of it just by asking for a weaker method (M5-33).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub enum Allow {
    /// SCRAM-SHA-256 with the server's signature verified, or none at all (trust, peer).
    #[default]
    Scram,
    /// SCRAM or md5.
    Md5,
    /// SCRAM, md5 or the password itself; the latter only over a secure channel.
    Password,
}

impl Allow {
    /// The setting `auth` of the module: `scram-sha-256` (default), `md5`, `password`.
    #[must_use]
    pub fn of(text: &str) -> Self {
        match text {
            "md5" => Self::Md5,
            "password" => Self::Password,
            _ => Self::Scram,
        }
    }
}

/// A session after the startup, ready for queries.
#[derive(Debug)]
pub struct Session<S> {
    io: S,
}

/// Where the authentication stands.
#[derive(Debug)]
enum Phase {
    /// Nothing asked yet.
    Start,
    /// The password or its md5 hash went out: only `AuthenticationOk` may follow.
    Sent,
    /// SCRAM started (10 answered): only the server-first-message (11) may follow.
    SaslStarted(Scram),
    /// The client-final-message went out: only the server's signature (12) may follow.
    SaslFinal(Scram),
    /// SCRAM verified: only `AuthenticationOk` may follow.
    Verified,
    /// `AuthenticationOk` taken: the parameters and `ReadyForQuery` follow.
    Done,
}

/// What the client does after a message of the startup.
#[derive(Debug, PartialEq, Eq)]
pub enum Step {
    /// Sends these bytes and reads on.
    Send(Vec<u8>),
    /// Reads on.
    Wait,
    /// `ReadyForQuery`: the session is open.
    Ready,
}

/// The startup and authentication as a pure state machine: what [`start`] runs over a connection,
/// fuzzed without one. A session opens only after `AuthenticationOk` in the right place: after
/// SCRAM only once the server proved it knows the password, never with a method weaker than
/// [`Startup::allow`], the password itself only over a secure channel.
pub struct Login<'a> {
    startup: Startup<'a>,
    phase: Phase,
    nonce: Option<String>,
}

impl std::fmt::Debug for Login<'_> {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Login")
            .field("startup", &self.startup)
            .field("phase", &self.phase)
            .finish_non_exhaustive()
    }
}

impl<'a> Login<'a> {
    /// The login of `startup` with a random SCRAM nonce.
    #[must_use]
    pub fn new(startup: Startup<'a>) -> Self {
        Self {
            startup,
            phase: Phase::Start,
            nonce: None,
        }
    }

    /// The same with a fixed SCRAM nonce (tests, fuzzing).
    #[must_use]
    pub fn with_nonce(startup: Startup<'a>, nonce: &str) -> Self {
        Self {
            nonce: Some(nonce.to_owned()),
            ..Self::new(startup)
        }
    }

    /// Whether the server proved itself or asked for nothing: `AuthenticationOk` was taken.
    #[must_use]
    pub fn authenticated(&self) -> bool {
        matches!(self.phase, Phase::Done)
    }

    /// The startup message.
    ///
    /// # Errors
    ///
    /// [`Error::Protocol`] for a NUL in a field.
    pub fn startup_message(&self) -> Result<Vec<u8>, Error> {
        let s = &self.startup;
        let mut body = Vec::new();
        body.extend_from_slice(&196_608i32.to_be_bytes());
        for (k, v) in [
            ("user", s.user),
            ("database", s.database),
            ("application_name", "sermon-agent"),
            ("options", s.options),
        ] {
            for part in [k, v] {
                if part.contains('\0') {
                    return Err(protocol("a NUL in the startup"));
                }
                body.extend_from_slice(part.as_bytes());
                body.push(0);
            }
        }
        body.push(0);
        let len = u32::try_from(body.len() + 4).unwrap_or(u32::MAX);
        Ok([&len.to_be_bytes()[..], &body].concat())
    }

    /// Takes the next message of the server.
    ///
    /// # Errors
    ///
    /// [`Error::Server`] for a refusal, [`Error::Protocol`] for a message out of order, a method
    /// weaker than allowed, or a server that can't prove it knows the password.
    pub fn feed(&mut self, m: &Message) -> Result<Step, Error> {
        match m.tag {
            b'R' => self.authenticate(&m.body),
            b'E' => {
                let (code, message) = error_fields(&m.body);
                Err(Error::Server { code, message })
            }
            b'S' | b'K' if self.authenticated() => Ok(Step::Wait),
            b'N' => Ok(Step::Wait),
            b'Z' if self.authenticated() => Ok(Step::Ready),
            other => Err(protocol(format!(
                "unexpected message {:?} in the startup",
                char::from(other)
            ))),
        }
    }

    fn password(&self) -> Result<&'a str, Error> {
        self.startup
            .password
            .ok_or_else(|| protocol("the server asks for a password: set the module's password"))
    }

    fn authenticate(&mut self, body: &[u8]) -> Result<Step, Error> {
        let mut b = Body(body);
        let method = b.i32()?;
        let allow = self.startup.allow;
        match (method, std::mem::replace(&mut self.phase, Phase::Start)) {
            (0, Phase::Start | Phase::Sent | Phase::Verified) => {
                self.phase = Phase::Done;
                Ok(Step::Wait)
            }
            (0, Phase::SaslStarted(_) | Phase::SaslFinal(_)) => Err(protocol(
                "SCRAM: the server ends the login without proving it knows the password",
            )),
            (3, Phase::Start) if allow == Allow::Password && self.startup.secure => {
                let reply = message(b'p', &[self.password()?.as_bytes(), b"\0"].concat());
                self.phase = Phase::Sent;
                Ok(Step::Send(reply))
            }
            (3, Phase::Start) => Err(protocol(
                "the server asks for the password in clear text (method password): allowed only with auth = password over the socket or tls = verify",
            )),
            (5, Phase::Start) if allow != Allow::Scram => {
                let salt = b.take(4)?;
                let hash = md5_password(self.startup.user, self.password()?, salt);
                self.phase = Phase::Sent;
                Ok(Step::Send(message(
                    b'p',
                    &[hash.as_bytes(), b"\0"].concat(),
                )))
            }
            (5, Phase::Start) => Err(protocol(
                "the server asks for md5: set password_encryption = scram-sha-256 and the role's password again, or allow auth = md5",
            )),
            (10, Phase::Start) => {
                let mut mechanisms = Vec::new();
                while let Ok(m) = b.cstr() {
                    if m.is_empty() {
                        break;
                    }
                    mechanisms.push(m);
                }
                if !mechanisms.contains(&b"SCRAM-SHA-256".as_slice()) {
                    return Err(protocol("the server offers no SCRAM-SHA-256"));
                }
                let nonce = match &self.nonce {
                    Some(n) => n.clone(),
                    None => nonce()?,
                };
                let state = Scram::new(self.password()?, &nonce);
                let first = state.client_first();
                let mut body = b"SCRAM-SHA-256\0".to_vec();
                body.extend_from_slice(
                    &i32::try_from(first.len()).unwrap_or(i32::MAX).to_be_bytes(),
                );
                body.extend_from_slice(first.as_bytes());
                self.phase = Phase::SaslStarted(state);
                Ok(Step::Send(message(b'p', &body)))
            }
            (11, Phase::SaslStarted(mut state)) => {
                let reply = state.client_final(&String::from_utf8_lossy(b.0))?;
                self.phase = Phase::SaslFinal(state);
                Ok(Step::Send(message(b'p', reply.as_bytes())))
            }
            (12, Phase::SaslFinal(state)) => {
                state.verify(&String::from_utf8_lossy(b.0))?;
                self.phase = Phase::Verified;
                Ok(Step::Wait)
            }
            (0 | 3 | 5 | 10 | 11 | 12, _) => Err(protocol("authentication out of order")),
            (other, _) => Err(protocol(format!(
                "authentication method {other} is not supported"
            ))),
        }
    }
}

/// Starts a session on `io`: the startup message and the authentication the server asks for
/// ([`Login`]).
///
/// # Errors
///
/// [`Error::Server`] if the server refuses (wrong password: 28P01), [`Error::Protocol`] for a
/// method this client doesn't speak or allow, or a server that can't prove it knows the password.
pub async fn start<S: AsyncRead + AsyncWrite + Unpin>(
    mut io: S,
    s: Startup<'_>,
) -> Result<Session<S>, Error> {
    let mut login = Login::new(s);
    io.write_all(&login.startup_message()?).await?;
    loop {
        let m = read(&mut io).await?;
        match login.feed(&m)? {
            Step::Send(reply) => io.write_all(&reply).await?,
            Step::Wait => {}
            Step::Ready => return Ok(Session { io }),
        }
    }
}

impl<S: AsyncRead + AsyncWrite + Unpin> Session<S> {
    /// Runs `sql` (one or more statements) and returns its result sets.
    ///
    /// # Errors
    ///
    /// [`Error::Server`] if a statement failed (e.g. 42501 without rights), [`Error`] else.
    pub async fn query(&mut self, sql: &str) -> Result<Vec<ResultSet>, Error> {
        if sql.contains('\0') {
            return Err(protocol("a NUL in the query"));
        }
        self.io
            .write_all(&message(b'Q', &[sql.as_bytes(), b"\0"].concat()))
            .await?;
        let mut q = Query::default();
        loop {
            let m = read(&mut self.io).await?;
            if let Some(answer) = q.feed(&m)? {
                return answer;
            }
        }
    }

    /// Ends the session (Terminate).
    pub async fn close(mut self) {
        let _ = self.io.write_all(&message(b'X', &[])).await;
        let _ = self.io.shutdown().await;
    }
}

/// `md5` + hex(md5(hex(md5(password + user)) + salt)), PostgreSQL's md5 method.
#[must_use]
pub fn md5_password(user: &str, password: &str, salt: &[u8]) -> String {
    let inner = hex(&md5(&[password.as_bytes(), user.as_bytes()].concat()));
    format!("md5{}", hex(&md5(&[inner.as_bytes(), salt].concat())))
}

fn hex(bytes: &[u8]) -> String {
    use std::fmt::Write;
    bytes.iter().fold(String::new(), |mut s, b| {
        let _ = write!(s, "{b:02x}");
        s
    })
}

/// The shifts of MD5 (RFC 1321).
const MD5_S: [u32; 64] = [
    7, 12, 17, 22, 7, 12, 17, 22, 7, 12, 17, 22, 7, 12, 17, 22, 5, 9, 14, 20, 5, 9, 14, 20, 5, 9,
    14, 20, 5, 9, 14, 20, 4, 11, 16, 23, 4, 11, 16, 23, 4, 11, 16, 23, 4, 11, 16, 23, 6, 10, 15,
    21, 6, 10, 15, 21, 6, 10, 15, 21, 6, 10, 15, 21,
];
/// MD5's constants: K[i] = floor(2^32 * |sin(i + 1)|).
const MD5_K: [u32; 64] = [
    0xd76a_a478,
    0xe8c7_b756,
    0x2420_70db,
    0xc1bd_ceee,
    0xf57c_0faf,
    0x4787_c62a,
    0xa830_4613,
    0xfd46_9501,
    0x6980_98d8,
    0x8b44_f7af,
    0xffff_5bb1,
    0x895c_d7be,
    0x6b90_1122,
    0xfd98_7193,
    0xa679_438e,
    0x49b4_0821,
    0xf61e_2562,
    0xc040_b340,
    0x265e_5a51,
    0xe9b6_c7aa,
    0xd62f_105d,
    0x0244_1453,
    0xd8a1_e681,
    0xe7d3_fbc8,
    0x21e1_cde6,
    0xc337_07d6,
    0xf4d5_0d87,
    0x455a_14ed,
    0xa9e3_e905,
    0xfcef_a3f8,
    0x676f_02d9,
    0x8d2a_4c8a,
    0xfffa_3942,
    0x8771_f681,
    0x6d9d_6122,
    0xfde5_380c,
    0xa4be_ea44,
    0x4bde_cfa9,
    0xf6bb_4b60,
    0xbebf_bc70,
    0x289b_7ec6,
    0xeaa1_27fa,
    0xd4ef_3085,
    0x0488_1d05,
    0xd9d4_d039,
    0xe6db_99e5,
    0x1fa2_7cf8,
    0xc4ac_5665,
    0xf429_2244,
    0x432a_ff97,
    0xab94_23a7,
    0xfc93_a039,
    0x655b_59c3,
    0x8f0c_cc92,
    0xffef_f47d,
    0x8584_5dd1,
    0x6fa8_7e4f,
    0xfe2c_e6e0,
    0xa301_4314,
    0x4e08_11a1,
    0xf753_7e82,
    0xbd3a_f235,
    0x2ad7_d2bb,
    0xeb86_d391,
];

/// MD5 (RFC 1321), only for PostgreSQL's md5 method: ring has none, and a crate for 40 lines
/// isn't worth its dependencies.
#[must_use]
#[allow(clippy::many_single_char_names, reason = "the names of RFC 1321")]
pub fn md5(data: &[u8]) -> [u8; 16] {
    let mut state: [u32; 4] = [0x6745_2301, 0xefcd_ab89, 0x98ba_dcfe, 0x1032_5476];
    let bits = u64::try_from(data.len())
        .unwrap_or(u64::MAX)
        .wrapping_mul(8);
    let mut padded = data.to_vec();
    padded.push(0x80);
    while padded.len() % 64 != 56 {
        padded.push(0);
    }
    padded.extend_from_slice(&bits.to_le_bytes());
    for chunk in padded.as_chunks::<64>().0 {
        let m: Vec<u32> = chunk
            .as_chunks::<4>()
            .0
            .iter()
            .map(|w| u32::from_le_bytes(*w))
            .collect();
        let [mut a, mut b, mut c, mut d] = state;
        for i in 0..64 {
            let (f, g) = match i / 16 {
                0 => ((b & c) | (!b & d), i),
                1 => ((d & b) | (!d & c), (5 * i + 1) % 16),
                2 => (b ^ c ^ d, (3 * i + 5) % 16),
                _ => (c ^ (b | !d), (7 * i) % 16),
            };
            let rotated = a
                .wrapping_add(f)
                .wrapping_add(MD5_K[i])
                .wrapping_add(m[g])
                .rotate_left(MD5_S[i]);
            (a, d, c) = (d, c, b);
            b = b.wrapping_add(rotated);
        }
        for (s, v) in state.iter_mut().zip([a, b, c, d]) {
            *s = s.wrapping_add(v);
        }
    }
    let mut out = [0u8; 16];
    for (o, s) in out.as_chunks_mut::<4>().0.iter_mut().zip(state) {
        o.copy_from_slice(&s.to_le_bytes());
    }
    out
}

fn nonce() -> Result<String, Error> {
    use ring::rand::SecureRandom;
    let mut bytes = [0u8; 18];
    ring::rand::SystemRandom::new()
        .fill(&mut bytes)
        .map_err(|_| protocol("no randomness for SCRAM"))?;
    Ok(B64.encode(bytes))
}

/// The client side of SCRAM-SHA-256 (RFC 5802, RFC 7677) without channel binding.
pub struct Scram {
    password: crate::Secret,
    nonce: String,
    user: String,
    expected: Option<[u8; 32]>,
}

impl std::fmt::Debug for Scram {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Scram").finish_non_exhaustive()
    }
}

impl Scram {
    /// A SCRAM exchange with `password` and the client's `nonce`; PostgreSQL takes the user of
    /// the startup and wants none here.
    #[must_use]
    pub fn new(password: &str, nonce: &str) -> Self {
        Self::for_user("", password, nonce)
    }

    /// The same with a user name in the messages (RFC 7677's test vector).
    #[must_use]
    pub fn for_user(user: &str, password: &str, nonce: &str) -> Self {
        Self {
            password: crate::Secret::new(password.to_owned()),
            nonce: nonce.to_owned(),
            user: user.to_owned(),
            expected: None,
        }
    }

    fn first_bare(&self) -> String {
        format!("n={},r={}", self.user, self.nonce)
    }

    /// The client-first-message.
    #[must_use]
    pub fn client_first(&self) -> String {
        format!("n,,{}", self.first_bare())
    }

    /// The client-final-message for the server-first-message `server`.
    ///
    /// # Errors
    ///
    /// [`Error::Protocol`] if `server` is broken, doesn't extend the client's nonce or asks for
    /// too many iterations.
    pub fn client_final(&mut self, server: &str) -> Result<String, Error> {
        let attr = |name: &str| {
            server
                .split(',')
                .find_map(|a| a.strip_prefix(name)?.strip_prefix('='))
                .ok_or_else(|| protocol(format!("SCRAM: no {name} from the server")))
        };
        let nonce = attr("r")?;
        if !nonce.starts_with(&self.nonce) || nonce.len() == self.nonce.len() {
            return Err(protocol("SCRAM: the server's nonce doesn't extend ours"));
        }
        let salt = B64
            .decode(attr("s")?)
            .map_err(|_| protocol("SCRAM: a broken salt"))?;
        let iterations = attr("i")?
            .parse::<u32>()
            .ok()
            .filter(|i| (1..=MAX_ITERATIONS).contains(i))
            .and_then(NonZeroU32::new)
            .ok_or_else(|| protocol("SCRAM: iterations out of bounds"))?;
        let mut salted = [0u8; 32];
        pbkdf2::derive(
            pbkdf2::PBKDF2_HMAC_SHA256,
            iterations,
            &salt,
            self.password.as_bytes(),
            &mut salted,
        );
        let key = hmac::Key::new(hmac::HMAC_SHA256, &salted);
        let client_key = hmac::sign(&key, b"Client Key");
        let stored = digest::digest(&digest::SHA256, client_key.as_ref());
        let without_proof = format!("c=biws,r={nonce}");
        let auth = format!("{},{server},{without_proof}", self.first_bare());
        let signature = hmac::sign(
            &hmac::Key::new(hmac::HMAC_SHA256, stored.as_ref()),
            auth.as_bytes(),
        );
        let proof: Vec<u8> = client_key
            .as_ref()
            .iter()
            .zip(signature.as_ref())
            .map(|(k, s)| k ^ s)
            .collect();
        let server_key = hmac::sign(&key, b"Server Key");
        let expected = hmac::sign(
            &hmac::Key::new(hmac::HMAC_SHA256, server_key.as_ref()),
            auth.as_bytes(),
        );
        let mut e = [0u8; 32];
        e.copy_from_slice(expected.as_ref());
        self.expected = Some(e);
        Ok(format!("{without_proof},p={}", B64.encode(proof)))
    }

    /// Checks the server-final-message: the server proves it knows the password too.
    ///
    /// # Errors
    ///
    /// [`Error::Protocol`] if the signature is missing or wrong.
    pub fn verify(&self, server: &str) -> Result<(), Error> {
        let expected = self
            .expected
            .ok_or_else(|| protocol("SCRAM out of order"))?;
        let got = server
            .strip_prefix("v=")
            .and_then(|v| B64.decode(v.trim_end_matches('\0')).ok())
            .ok_or_else(|| protocol("SCRAM: no server signature"))?;
        // Compared in constant time: every byte, whatever differs first.
        let differ = got.len() != expected.len()
            || got
                .iter()
                .zip(expected)
                .fold(0u8, |acc, (a, b)| acc | (a ^ b))
                != 0;
        if differ {
            return Err(protocol("SCRAM: the server's signature is wrong"));
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests;
