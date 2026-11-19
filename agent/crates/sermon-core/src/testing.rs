//! Fakes for the ports of this crate, for tests in all agent crates (feature `testing`).

use std::sync::{Mutex, MutexGuard, PoisonError};
use std::time::{Duration, SystemTime};

use crate::clock::{Clock, Monotonic};

/// A clock that only moves when the test says so.
#[derive(Debug)]
pub struct FakeClock {
    state: Mutex<State>,
}

#[derive(Debug, Clone, Copy)]
struct State {
    wall: SystemTime,
    monotonic: Monotonic,
}

impl FakeClock {
    /// A clock showing `wall`, with the monotonic time at its origin.
    #[must_use]
    pub fn new(wall: SystemTime) -> Self {
        Self {
            state: Mutex::new(State {
                wall,
                monotonic: Monotonic::default(),
            }),
        }
    }

    /// A clock at 2026-11-19T00:00:00Z.
    #[must_use]
    pub fn at_default_time() -> Self {
        Self::new(SystemTime::UNIX_EPOCH + Duration::from_hours(498_624))
    }

    /// Lets `duration` pass: wall-clock and monotonic time move together.
    ///
    /// # Panics
    ///
    /// If the time overflows – a bug in the test.
    #[allow(
        clippy::panic,
        reason = "test helper: an overflow is a bug in the test"
    )]
    pub fn advance(&self, duration: Duration) {
        let mut state = self.lock();
        state.wall = state
            .wall
            .checked_add(duration)
            .unwrap_or_else(|| panic!("FakeClock: wall clock overflow"));
        state.monotonic = state
            .monotonic
            .checked_add(duration)
            .unwrap_or_else(|| panic!("FakeClock: monotonic clock overflow"));
    }

    /// Sets the wall clock only, like an NTP step; the monotonic time is unaffected.
    pub fn set_wall(&self, wall: SystemTime) {
        self.lock().wall = wall;
    }

    fn lock(&self) -> MutexGuard<'_, State> {
        // A panicking test thread must not hide the clock from the others.
        self.state.lock().unwrap_or_else(PoisonError::into_inner)
    }
}

impl Clock for FakeClock {
    fn now(&self) -> SystemTime {
        self.lock().wall
    }

    fn monotonic(&self) -> Monotonic {
        self.lock().monotonic
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn advance_moves_wall_and_monotonic_time_together() {
        let clock = FakeClock::at_default_time();
        let wall = clock.now();

        clock.advance(Duration::from_secs(90));

        assert_eq!(clock.now(), wall + Duration::from_secs(90));
        assert_eq!(
            clock.monotonic(),
            Monotonic::from_origin(Duration::from_secs(90))
        );
    }

    #[test]
    fn set_wall_does_not_move_monotonic_time() {
        let clock = FakeClock::at_default_time();
        clock.advance(Duration::from_secs(1));

        clock.set_wall(SystemTime::UNIX_EPOCH);

        assert_eq!(clock.now(), SystemTime::UNIX_EPOCH);
        assert_eq!(
            clock.monotonic(),
            Monotonic::from_origin(Duration::from_secs(1))
        );
    }
}
