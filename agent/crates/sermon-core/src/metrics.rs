//! Metric samples as produced by the collectors (KONZEPT §8).
//!
//! Names follow `sermon_<area>_<name>_<unit>`. Counters are converted to rates by the agent;
//! every sample is a gauge. Values are integers in base units where possible (bytes/s, counts);
//! ratios are integers in ppm ([`PPM`] = 100 %).

/// One million: a ratio of 1.0 in parts per million.
pub const PPM: i64 = 1_000_000;

/// A measured value.
#[derive(Debug, Clone, Copy, PartialEq)]
pub enum Value {
    /// An integer in base units (preferred, compresses well).
    Int(i64),
    /// A fractional value (load averages, latencies, small event rates).
    Float(f64),
}

/// One value of one series. Organization and node are added by the server.
#[derive(Debug, Clone, PartialEq)]
pub struct Sample {
    /// Metric name, e.g. `sermon_cpu_utilization_ratio`.
    pub metric: &'static str,
    /// Bounded technical labels, e.g. `("mode", "user")`.
    pub labels: Vec<(&'static str, String)>,
    /// The value.
    pub value: Value,
}

impl Sample {
    /// An integer sample without labels.
    #[must_use]
    pub fn int(metric: &'static str, value: i64) -> Self {
        Self {
            metric,
            labels: Vec::new(),
            value: Value::Int(value),
        }
    }

    /// A fractional sample without labels; non-finite values become 0 (the protocol forbids
    /// NaN and infinity).
    #[must_use]
    pub fn float(metric: &'static str, value: f64) -> Self {
        Self {
            metric,
            labels: Vec::new(),
            value: Value::Float(if value.is_finite() { value } else { 0.0 }),
        }
    }

    /// Adds a label.
    #[must_use]
    pub fn label(mut self, name: &'static str, value: impl Into<String>) -> Self {
        self.labels.push((name, value.into()));
        self
    }

    /// Identity of the series (metric and labels), for the cardinality limit.
    #[must_use]
    pub fn series_key(&self) -> String {
        let mut labels: Vec<_> = self.labels.iter().collect();
        labels.sort();
        let mut key = self.metric.to_owned();
        for (name, value) in labels {
            key.push('\u{1f}');
            key.push_str(name);
            key.push('=');
            key.push_str(value);
        }
        key
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn series_key_ignores_label_order() {
        let a = Sample::int("sermon_x", 1)
            .label("cpu", "0")
            .label("mode", "user");
        let b = Sample::int("sermon_x", 2)
            .label("mode", "user")
            .label("cpu", "0");
        let c = Sample::int("sermon_x", 2)
            .label("mode", "system")
            .label("cpu", "0");

        assert_eq!(a.series_key(), b.series_key());
        assert_ne!(a.series_key(), c.series_key());
    }

    #[test]
    fn non_finite_floats_become_zero() {
        assert_eq!(Sample::float("sermon_x", f64::NAN).value, Value::Float(0.0));
        assert_eq!(
            Sample::float("sermon_x", f64::INFINITY).value,
            Value::Float(0.0)
        );
        assert_eq!(Sample::float("sermon_x", 0.5).value, Value::Float(0.5));
    }
}
