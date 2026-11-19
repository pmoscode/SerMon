//! Exponential backoff with full jitter for reconnects.
//!
//! Full jitter (a random delay between zero and the exponential bound) spreads the reconnects of
//! many agents after a server restart instead of letting them arrive in waves.

use std::time::Duration;

/// Reconnect delays: `random(0 ..= min(max, initial · 2^attempt))`.
#[derive(Debug, Clone)]
pub struct Backoff {
    initial: Duration,
    max: Duration,
    attempt: u32,
}

impl Backoff {
    /// A backoff starting at `initial` and never exceeding `max`.
    #[must_use]
    pub fn new(initial: Duration, max: Duration) -> Self {
        Self {
            initial,
            max: max.max(initial),
            attempt: 0,
        }
    }

    /// The delay before the next attempt; `random` is a uniformly distributed number.
    pub fn next_delay(&mut self, random: u64) -> Duration {
        let bound = self.bound();
        self.attempt = self.attempt.saturating_add(1);
        let bound_nanos = u64::try_from(bound.as_nanos()).unwrap_or(u64::MAX);
        Duration::from_nanos(random % bound_nanos.saturating_add(1))
    }

    /// Starts over after a successful connection.
    pub fn reset(&mut self) {
        self.attempt = 0;
    }

    /// The upper bound of the next delay.
    #[must_use]
    pub fn bound(&self) -> Duration {
        let factor = 2_u32.checked_pow(self.attempt.min(31)).unwrap_or(u32::MAX);
        self.initial
            .checked_mul(factor)
            .map_or(self.max, |delay| delay.min(self.max))
    }
}

/// A random number from the operating system (for the jitter; not security relevant).
#[must_use]
pub fn random_u64() -> u64 {
    use ring::rand::SecureRandom;
    let mut bytes = [0; 8];
    // On failure the delay is simply the bound's lower end; reconnecting still works.
    let _ = ring::rand::SystemRandom::new().fill(&mut bytes);
    u64::from_le_bytes(bytes)
}

#[cfg(test)]
mod tests {
    use proptest::prelude::*;

    use super::*;

    #[test]
    fn bound_doubles_up_to_the_maximum_and_resets() {
        let mut backoff = Backoff::new(Duration::from_secs(1), Duration::from_secs(60));
        let mut bounds = Vec::new();
        for _ in 0..8 {
            bounds.push(backoff.bound().as_secs());
            backoff.next_delay(0);
        }
        assert_eq!(bounds, vec![1, 2, 4, 8, 16, 32, 60, 60]);

        backoff.reset();
        assert_eq!(backoff.bound(), Duration::from_secs(1));
    }

    #[test]
    fn jitter_covers_zero_to_the_bound() {
        let mut backoff = Backoff::new(Duration::from_secs(1), Duration::from_secs(60));
        assert_eq!(backoff.next_delay(0), Duration::ZERO);
        let mut backoff = Backoff::new(Duration::from_secs(1), Duration::from_secs(60));
        assert_eq!(backoff.next_delay(1_000_000_000), Duration::from_secs(1));
    }

    #[test]
    fn many_attempts_do_not_overflow() {
        let mut backoff = Backoff::new(Duration::from_secs(1), Duration::from_secs(60));
        for _ in 0..1000 {
            backoff.next_delay(u64::MAX);
        }
        assert_eq!(backoff.bound(), Duration::from_secs(60));
    }

    proptest! {
        #[test]
        fn delay_never_exceeds_the_bound(attempts in 0..100_u32, random: u64) {
            let mut backoff = Backoff::new(Duration::from_millis(500), Duration::from_secs(60));
            for _ in 0..attempts {
                backoff.next_delay(random);
            }
            let bound = backoff.bound();
            prop_assert!(backoff.next_delay(random) <= bound);
            prop_assert!(bound <= Duration::from_secs(60));
        }
    }
}
