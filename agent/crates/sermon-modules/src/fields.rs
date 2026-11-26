//! Reading the fields of a configuration that follows its schema (shared by the modules).

use std::time::Duration;

use serde_json::Value;

use crate::{BuildError, Config, schema};

/// Longest timeout of a request: the run's budget is at most 30 s.
pub const MAX_TIMEOUT: Duration = Duration::from_secs(25);

/// The strings of the list `name`.
pub fn strings<'a>(config: &'a Config, name: &str) -> impl Iterator<Item = &'a str> {
    config
        .get(name)
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .filter_map(Value::as_str)
}

/// The duration `name`, at most [`MAX_TIMEOUT`].
///
/// # Errors
///
/// [`BuildError::Invalid`] for no duration, zero or a longer one.
pub fn timeout(config: &Config, name: &str, default: Duration) -> Result<Duration, BuildError> {
    let timeout = match config.get(name).and_then(Value::as_str) {
        None => default,
        Some(text) => {
            schema::duration(text).map_err(|e| BuildError::Invalid(format!("{name}: {e}")))?
        }
    };
    if timeout.is_zero() || timeout > MAX_TIMEOUT {
        return Err(BuildError::Invalid(format!(
            "{name}: more than 0 and at most {} s",
            MAX_TIMEOUT.as_secs()
        )));
    }
    Ok(timeout)
}
