//! Time synchronisation from the kernel's NTP state (`adjtimex`, M2-22, KONZEPT §8.1): whether the
//! clock is synchronised, its offset and the kernel's bound of the error. Works the same with
//! chrony, ntpd and systemd-timesyncd, which all steer the kernel clock. Runs every 10 s.
//!
//! | Metric | Labels | Unit |
//! |---|---|---|
//! | `sermon_time_synchronized` | – | boolean |
//! | `sermon_time_offset_seconds` | – | seconds (last offset the NTP daemon passed to the kernel) |
//! | `sermon_time_max_error_seconds` | – | seconds (grows while nothing synchronises) |
//!
//! The agent only reads (`modes = 0`); setting the clock needs `CAP_SYS_TIME`, which it does not
//! have. The systemd unit allows `adjtimex` and `clock_adjtime` explicitly (ADR 0026).

use std::io;

use sermon_core::Sample;

use crate::rate::to_f64;
use crate::{CollectError, Collector, Context};

/// What the kernel says about its clock.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct KernelClock {
    /// The return value of `adjtimex`: the clock state (`TIME_ERROR` = 5: not synchronised).
    pub state: i32,
    /// `timex.status` (`STA_UNSYNC` = 0x40, `STA_NANO` = 0x2000).
    pub status: i32,
    /// `timex.offset`: microseconds, nanoseconds with `STA_NANO`.
    pub offset: i64,
    /// `timex.maxerror` in microseconds.
    pub max_error_us: i64,
}

const STA_UNSYNC: i32 = 0x0040;
const STA_NANO: i32 = 0x2000;
const TIME_ERROR: i32 = 5;

impl KernelClock {
    /// Synchronised: the kernel neither reports an error state nor the unsynchronised flag.
    #[must_use]
    pub fn synchronized(&self) -> bool {
        self.state != TIME_ERROR && self.status & STA_UNSYNC == 0
    }

    /// The offset in seconds.
    #[must_use]
    pub fn offset_seconds(&self) -> f64 {
        let per_second = if self.status & STA_NANO == 0 {
            1e6
        } else {
            1e9
        };
        signed(self.offset) / per_second
    }

    /// The samples of this state.
    #[must_use]
    pub fn samples(&self) -> Vec<Sample> {
        let max_error = signed(self.max_error_us) / 1e6;
        vec![
            Sample::int("sermon_time_synchronized", i64::from(self.synchronized())),
            Sample::float("sermon_time_offset_seconds", self.offset_seconds()),
            Sample::float("sermon_time_max_error_seconds", max_error),
        ]
    }
}

/// A signed kernel value as `f64` (offsets and errors are far below 2^53).
fn signed(value: i64) -> f64 {
    let magnitude = to_f64(value.unsigned_abs());
    if value < 0 { -magnitude } else { magnitude }
}

/// Where the state comes from: the kernel, or a fake in tests.
pub trait ClockSource: Send {
    /// The current state.
    ///
    /// # Errors
    ///
    /// The call is not possible (other OS, syscall filter).
    fn read(&self) -> io::Result<KernelClock>;
}

/// The kernel, through `adjtimex(2)` with `modes = 0` (read only).
#[derive(Debug, Default, Clone, Copy)]
pub struct Adjtimex;

impl ClockSource for Adjtimex {
    #[cfg(target_os = "linux")]
    #[allow(
        unsafe_code,
        reason = "adjtimex has no safe wrapper in rustix (ADR 0026)"
    )]
    fn read(&self) -> io::Result<KernelClock> {
        // SAFETY: `timex` is plain old data, all-zero is a valid value and means `modes = 0`, so the
        // kernel only fills the struct and changes nothing. The pointer is to a live local value
        // for the duration of the call.
        let mut timex: libc::timex = unsafe { std::mem::zeroed() };
        // SAFETY: see above; adjtimex writes only into `timex`.
        let state = unsafe { libc::adjtimex(&raw mut timex) };
        if state < 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(KernelClock {
            state,
            status: timex.status,
            // c_long: 64 bits on the targets (amd64, arm64).
            offset: timex.offset,
            max_error_us: timex.maxerror,
        })
    }

    #[cfg(not(target_os = "linux"))]
    fn read(&self) -> io::Result<KernelClock> {
        Err(io::ErrorKind::Unsupported.into())
    }
}

/// Time synchronisation collector. Without access to the state (other OS, syscall filter of an
/// older unit) it yields nothing and says so once.
pub struct TimeSyncCollector {
    source: Box<dyn ClockSource>,
    warned: bool,
}

impl std::fmt::Debug for TimeSyncCollector {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("TimeSyncCollector").finish_non_exhaustive()
    }
}

impl TimeSyncCollector {
    /// A collector reading from `source`.
    #[must_use]
    pub fn new(source: Box<dyn ClockSource>) -> Self {
        Self {
            source,
            warned: false,
        }
    }
}

impl Default for TimeSyncCollector {
    fn default() -> Self {
        Self::new(Box::new(Adjtimex))
    }
}

impl Collector for TimeSyncCollector {
    fn name(&self) -> &'static str {
        "timesync"
    }

    fn interval_ticks(&self) -> u32 {
        10
    }

    fn collect(&mut self, _: &Context<'_>, out: &mut Vec<Sample>) -> Result<(), CollectError> {
        match self.source.read() {
            Ok(clock) => out.extend(clock.samples()),
            Err(error) => {
                if !std::mem::replace(&mut self.warned, true) {
                    tracing::debug!("no time synchronisation state: {error}");
                }
            }
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use sermon_core::{ProcRoot, Value};

    use super::*;
    use crate::testing::{at, value};

    struct Fake(io::Result<KernelClock>);

    impl ClockSource for Fake {
        fn read(&self) -> io::Result<KernelClock> {
            match &self.0 {
                Ok(clock) => Ok(*clock),
                Err(error) => Err(io::Error::new(error.kind(), "fake")),
            }
        }
    }

    fn collect(source: Fake) -> Vec<Sample> {
        let root = ProcRoot::new("/nonexistent");
        let context = Context {
            root: &root,
            now: at(0),
        };
        let mut out = Vec::new();
        TimeSyncCollector::new(Box::new(source))
            .collect(&context, &mut out)
            .unwrap();
        out
    }

    #[test]
    fn a_synchronised_clock_reports_its_offset_and_error() {
        // chrony: TIME_OK, PLL, offset 1 234 µs, maxerror 0.25 s.
        let out = collect(Fake(Ok(KernelClock {
            state: 0,
            status: 0x0001,
            offset: 1234,
            max_error_us: 250_000,
        })));
        assert_eq!(
            value(&out, "sermon_time_synchronized", &[]),
            Some(Value::Int(1))
        );
        assert_eq!(
            value(&out, "sermon_time_offset_seconds", &[]),
            Some(Value::Float(0.001_234))
        );
        assert_eq!(
            value(&out, "sermon_time_max_error_seconds", &[]),
            Some(Value::Float(0.25))
        );
        // Nanosecond resolution, negative offset.
        let nano = KernelClock {
            state: 0,
            status: STA_NANO,
            offset: -2_500_000,
            max_error_us: 0,
        };
        assert!((nano.offset_seconds() + 0.0025).abs() < 1e-12);
    }

    #[test]
    fn an_unsynchronised_clock_is_reported_as_such() {
        for clock in [
            // No NTP daemon: the kernel's initial state.
            KernelClock {
                state: TIME_ERROR,
                status: STA_UNSYNC,
                offset: 0,
                max_error_us: 16_000_000,
            },
            // The flag alone.
            KernelClock {
                state: 0,
                status: STA_UNSYNC,
                offset: 0,
                max_error_us: 1,
            },
            // The error state alone (leap second handling went wrong).
            KernelClock {
                state: TIME_ERROR,
                status: 0,
                offset: 0,
                max_error_us: 1,
            },
        ] {
            let out = collect(Fake(Ok(clock)));
            assert_eq!(
                value(&out, "sermon_time_synchronized", &[]),
                Some(Value::Int(0)),
                "{clock:?}"
            );
        }
    }

    #[test]
    fn without_access_nothing_is_reported() {
        let out = collect(Fake(Err(io::ErrorKind::PermissionDenied.into())));
        assert!(out.is_empty(), "{out:?}");
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn the_kernel_answers() {
        let clock = Adjtimex.read().unwrap();
        assert!((0..=5).contains(&clock.state), "{clock:?}");
    }
}
