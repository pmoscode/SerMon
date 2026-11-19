//! Rates and ratios from counter deltas (KONZEPT §8): counters become per-second gauges in the
//! agent. A counter that went backwards was reset (reboot of a device, driver reload, 32-bit wrap)
//! – then there is no value for this interval instead of a huge or negative one.

use std::time::Duration;

use sermon_core::PPM;

/// Increase from `previous` to `current`; `None` if the counter went backwards.
#[must_use]
pub fn delta(previous: u64, current: u64) -> Option<u64> {
    current.checked_sub(previous)
}

/// A counter value as `f64` (rates and averages don't need 64-bit precision).
#[must_use]
#[allow(
    clippy::as_conversions,
    clippy::cast_precision_loss,
    reason = "u64 → f64 has no lossless conversion; the precision is enough for rates"
)]
pub fn to_f64(value: u64) -> f64 {
    value as f64
}

/// A value rounded to `i64`, saturating at the bounds; NaN becomes 0.
#[must_use]
#[allow(
    clippy::as_conversions,
    clippy::cast_possible_truncation,
    reason = "float → int `as` saturates and maps NaN to 0, which is what is wanted here"
)]
pub fn round_to_i64(value: f64) -> i64 {
    value.round() as i64
}

/// Per-second rate as an integer (rounded); `None` on reset or without elapsed time.
#[must_use]
pub fn per_second(previous: u64, current: u64, elapsed: Duration) -> Option<i64> {
    per_second_float(previous, current, elapsed).map(round_to_i64)
}

/// Per-second rate as a fraction (for small event rates); `None` on reset or without time.
#[must_use]
pub fn per_second_float(previous: u64, current: u64, elapsed: Duration) -> Option<f64> {
    let increase = delta(previous, current)?;
    let seconds = elapsed.as_secs_f64();
    (seconds > 0.0).then(|| to_f64(increase) / seconds)
}

/// `part / whole` in ppm, rounded and capped at 100 %; `None` if `whole` is zero.
#[must_use]
pub fn ppm(part: u64, whole: u64) -> Option<i64> {
    if whole == 0 {
        return None;
    }
    let scaled = u128::from(part.min(whole)) * 1_000_000 + u128::from(whole) / 2;
    i64::try_from(scaled / u128::from(whole))
        .ok()
        .map(|value| value.min(PPM))
}

/// `part / elapsed` in ppm, for time counters (e.g. microseconds stalled per elapsed time).
#[must_use]
pub fn time_ppm(part: Duration, elapsed: Duration) -> Option<i64> {
    let part = u64::try_from(part.as_nanos()).ok()?;
    let whole = u64::try_from(elapsed.as_nanos()).ok()?;
    ppm(part, whole)
}

#[cfg(test)]
mod tests {
    use proptest::prelude::*;

    use super::*;

    #[test]
    fn rates_per_second() {
        let second = Duration::from_secs(1);
        assert_eq!(per_second(100, 1100, second), Some(1000));
        assert_eq!(
            per_second(100, 1100, Duration::from_millis(500)),
            Some(2000)
        );
        assert_eq!(per_second(100, 101, Duration::from_secs(3)), Some(0));
        assert_eq!(
            per_second_float(100, 101, Duration::from_secs(4)),
            Some(0.25)
        );
    }

    #[test]
    fn a_reset_or_no_elapsed_time_gives_no_value() {
        assert_eq!(per_second(1000, 10, Duration::from_secs(1)), None);
        assert_eq!(per_second(10, 20, Duration::ZERO), None);
        assert_eq!(delta(u64::MAX, 0), None);
    }

    #[test]
    fn ratios_in_ppm() {
        assert_eq!(ppm(1, 4), Some(250_000));
        assert_eq!(ppm(1, 3), Some(333_333));
        assert_eq!(ppm(2, 3), Some(666_667));
        assert_eq!(ppm(5, 4), Some(PPM), "capped");
        assert_eq!(ppm(0, 0), None);
        assert_eq!(ppm(u64::MAX, u64::MAX), Some(PPM));
        assert_eq!(
            time_ppm(Duration::from_millis(30), Duration::from_secs(1)),
            Some(30_000)
        );
    }

    proptest! {
        #[test]
        fn rates_are_never_negative(previous: u64, current: u64, millis in 0..10_000_u64) {
            let rate = per_second(previous, current, Duration::from_millis(millis));
            prop_assert!(rate.is_none_or(|rate| rate >= 0));
        }

        #[test]
        fn ppm_stays_within_bounds(part: u64, whole: u64) {
            prop_assert!(ppm(part, whole).is_none_or(|value| (0..=PPM).contains(&value)));
        }
    }
}
