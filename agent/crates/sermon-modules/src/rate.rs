//! Rates of counters of services between two runs (nginx requests, Redis commands): per second
//! on the agent's monotonic clock; none at the first run and after a reset (the service
//! restarted).

use std::time::Duration;

use sermon_core::Monotonic;

/// A counter and when it was read last.
#[derive(Debug, Clone, Copy, Default)]
pub struct Counter {
    last: Option<(u64, Monotonic)>,
}

impl Counter {
    /// The rate since the last reading of `value` at `now`, then remembers it.
    pub fn per_second(&mut self, value: u64, now: Monotonic) -> Option<f64> {
        let rate = self.last.and_then(|(before, then)| {
            let elapsed = now.saturating_duration_since(then);
            (value >= before && elapsed >= Duration::from_millis(100))
                .then(|| u64_to_f64(value - before) / elapsed.as_secs_f64())
        });
        self.last = Some((value, now));
        rate
    }
}

/// `n` as a float; above 2^53 rounded, which a rate doesn't feel.
#[allow(clippy::cast_precision_loss, clippy::as_conversions)]
fn u64_to_f64(n: u64) -> f64 {
    n as f64
}

#[cfg(test)]
mod tests {
    use super::*;

    fn at(secs: u64) -> Monotonic {
        Monotonic::from_origin(Duration::from_secs(secs))
    }

    #[test]
    fn rates_between_readings() {
        let mut c = Counter::default();
        assert_eq!(c.per_second(100, at(10)), None, "the first reading");
        assert_eq!(c.per_second(400, at(40)), Some(10.0));
        assert_eq!(c.per_second(400, at(70)), Some(0.0));
        assert_eq!(c.per_second(5, at(100)), None, "a reset");
        assert_eq!(
            c.per_second(65, at(130)),
            Some(2.0),
            "counts again after a reset"
        );
        assert_eq!(c.per_second(70, at(130)), None, "no time passed");
    }
}
