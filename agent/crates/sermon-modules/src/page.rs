//! A status page of a service over the modules' HTTP client (nginx `stub_status`, Apache
//! `server-status`): the client of a configuration with `url`, `timeout`, `tls_verify` and
//! `ca_file`, and one read of the page.

use std::time::Duration;

use serde_json::Value;

use crate::fields::timeout;
use crate::http::{Client, HttpError, Options, Url};
use crate::{BuildError, Config, RunError, Setup};

/// The client and URL of a status page; `default_url` when the configuration has none. The URL
/// has no query but `query` (M5-33): a status page needs no parameters, a GET with them to
/// another service on loopback could change something there.
///
/// # Errors
///
/// [`BuildError`] for a bad URL, another query or timeout, a CA file outside the veto.
pub fn client(
    config: &Config,
    setup: &Setup<'_>,
    default_url: &str,
    query: Option<&str>,
    accept: &'static str,
) -> Result<(Client, Url, Duration), BuildError> {
    let text = config
        .get("url")
        .and_then(Value::as_str)
        .unwrap_or(default_url);
    let url = Url::parse(text).map_err(|e| BuildError::Invalid(format!("url: {e}")))?;
    if url.query().is_some_and(|q| Some(q) != query) {
        return Err(BuildError::Invalid(match query {
            Some(q) => format!("url: no query but ?{q}"),
            None => "url: no query".into(),
        }));
    }
    let timeout = timeout(config, "timeout", Duration::from_secs(5))?;
    let client = Client::new(
        setup.veto,
        Options {
            timeout,
            tls_verify: config
                .get("tls_verify")
                .and_then(Value::as_bool)
                .unwrap_or(true),
            ca_file: config
                .get("ca_file")
                .and_then(Value::as_str)
                .map(Into::into),
            accept,
            ..Options::default()
        },
    )?;
    Ok((client, url, timeout))
}

/// The page as text, `None` if the service doesn't answer (down).
///
/// # Errors
///
/// [`RunError::Vetoed`] outside the veto, [`RunError::Failed`] for a status other than 200
/// (`hint` says what to check), a bad answer or one that is no text.
pub async fn read(client: &Client, url: &Url, hint: &str) -> Result<Option<String>, RunError> {
    let response = match client.get(url, true).await {
        Ok(response) => response,
        Err(HttpError::Vetoed(r)) => return Err(RunError::Vetoed(r)),
        Err(HttpError::Failed(why)) => return Err(RunError::Failed(why)),
        Err(HttpError::Unreachable(_)) => return Ok(None),
    };
    if response.status != 200 {
        return Err(RunError::Failed(format!(
            "{}: status {} – {hint}",
            url.label(),
            response.status
        )));
    }
    String::from_utf8(response.body.to_vec())
        .map(Some)
        .map_err(|_| RunError::Failed(format!("{}: not a text", url.label())))
}
