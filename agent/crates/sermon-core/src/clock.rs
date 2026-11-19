//! Port `Clock`: the only source of time in the agent, so that tests never depend on real time.

use std::fmt::Debug;
use std::time::{Duration, Instant, SystemTime};

/// A point on the monotonic time line: the time elapsed since an arbitrary but fixed origin.
///
/// Used for intervals, deadlines and rates. Unlike [`SystemTime`] it never jumps, also not when NTP
/// steps the wall clock.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash, Default)]
pub struct Monotonic(Duration);

impl Monotonic {
    /// The point `elapsed` after the origin of the clock.
    #[must_use]
    pub const fn from_origin(elapsed: Duration) -> Self {
        Self(elapsed)
    }

    /// Time since the origin of the clock.
    #[must_use]
    pub const fn since_origin(self) -> Duration {
        self.0
    }

    /// Time from `earlier` to `self`; zero if `earlier` is later.
    #[must_use]
    pub fn saturating_duration_since(self, earlier: Self) -> Duration {
        self.0.saturating_sub(earlier.0)
    }

    /// The point `duration` after `self`, or `None` on overflow.
    #[must_use]
    pub fn checked_add(self, duration: Duration) -> Option<Self> {
        self.0.checked_add(duration).map(Self)
    }
}

/// Source of wall-clock and monotonic time.
pub trait Clock: Send + Sync + Debug {
    /// Wall-clock time for timestamps of samples, events and heartbeats. May jump (NTP).
    fn now(&self) -> SystemTime;

    /// Monotonic time for intervals and deadlines. Never goes backwards.
    fn monotonic(&self) -> Monotonic;
}

/// The real clock of the operating system.
#[derive(Debug, Clone, Copy)]
pub struct SystemClock {
    origin: Instant,
}

impl SystemClock {
    /// A clock whose monotonic origin is the moment of creation.
    #[must_use]
    pub fn new() -> Self {
        Self {
            origin: Instant::now(),
        }
    }
}

impl Default for SystemClock {
    fn default() -> Self {
        Self::new()
    }
}

impl Clock for SystemClock {
    fn now(&self) -> SystemTime {
        SystemTime::now()
    }

    fn monotonic(&self) -> Monotonic {
        Monotonic(self.origin.elapsed())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn saturating_duration_since_is_zero_for_a_later_point() {
        let earlier = Monotonic::from_origin(Duration::from_secs(5));
        let later = Monotonic::from_origin(Duration::from_secs(7));

        assert_eq!(
            later.saturating_duration_since(earlier),
            Duration::from_secs(2)
        );
        assert_eq!(earlier.saturating_duration_since(later), Duration::ZERO);
    }

    #[test]
    fn checked_add_detects_overflow() {
        let point = Monotonic::from_origin(Duration::MAX);

        assert_eq!(point.checked_add(Duration::from_nanos(1)), None);
        assert_eq!(
            Monotonic::default().checked_add(Duration::from_secs(1)),
            Some(Monotonic::from_origin(Duration::from_secs(1)))
        );
    }

    #[test]
    fn system_clock_is_monotonic() {
        let clock = SystemClock::new();

        let first = clock.monotonic();
        let second = clock.monotonic();

        assert!(second >= first);
    }
}
