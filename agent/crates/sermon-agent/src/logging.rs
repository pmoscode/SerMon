//! The agent's own log output: plain text on stdout, collected by journald (or OpenRC's logger).

use std::io::IsTerminal;

use tracing::level_filters::LevelFilter;

use crate::config::LogLevel;

/// Installs the global log subscriber. A second call has no effect.
pub fn init(level: LogLevel) {
    // journald stamps every line itself; it sets JOURNAL_STREAM for services it captures.
    let under_journald = std::env::var_os("JOURNAL_STREAM").is_some();
    let builder = tracing_subscriber::fmt()
        .with_max_level(level_filter(level))
        .with_writer(std::io::stdout)
        .with_ansi(std::io::stdout().is_terminal())
        .with_target(false);
    // Ignored error: a subscriber is already installed (tests, repeated calls).
    let _ = if under_journald {
        builder.without_time().try_init()
    } else {
        builder.try_init()
    };
}

fn level_filter(level: LogLevel) -> LevelFilter {
    match level {
        LogLevel::Error => LevelFilter::ERROR,
        LogLevel::Warn => LevelFilter::WARN,
        LogLevel::Info => LevelFilter::INFO,
        LogLevel::Debug => LevelFilter::DEBUG,
        LogLevel::Trace => LevelFilter::TRACE,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn maps_every_level() {
        assert_eq!(level_filter(LogLevel::Error), LevelFilter::ERROR);
        assert_eq!(level_filter(LogLevel::Warn), LevelFilter::WARN);
        assert_eq!(level_filter(LogLevel::Info), LevelFilter::INFO);
        assert_eq!(level_filter(LogLevel::Debug), LevelFilter::DEBUG);
        assert_eq!(level_filter(LogLevel::Trace), LevelFilter::TRACE);
    }
}
