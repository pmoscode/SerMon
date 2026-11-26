//! The Prometheus text format (0.0.4) and `OpenMetrics` 1.0, as the scraper reads them (M5-19):
//! strict per line, lenient per page – a line that doesn't parse is skipped and counted, not the
//! whole page; `# EOF` ends it. Bounded: at most [`MAX_LINE`] bytes a line, [`MAX_LABELS`]
//! labels a series, [`MAX_FAMILIES`] types remembered.

use std::collections::BTreeMap;

/// Longest line taken.
pub const MAX_LINE: usize = 16 * 1024;
/// Most labels of a series taken (more: the series is skipped).
pub const MAX_LABELS: usize = 16;
/// Most `# TYPE` lines remembered.
pub const MAX_FAMILIES: usize = 4096;

/// The type of a metric family (`# TYPE`).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Kind {
    /// Only goes up: the scraper reports its rate.
    Counter,
    /// A value as it is.
    Gauge,
    /// Buckets, `_sum` and `_count`.
    Histogram,
    /// Quantiles, `_sum` and `_count`.
    Summary,
    /// Without `# TYPE`, `unknown`, `info`, `stateset`.
    Untyped,
}

/// A series of the page.
#[derive(Debug, Clone, PartialEq)]
pub struct Series {
    /// The metric name, e.g. `http_requests_total`.
    pub name: String,
    /// The labels, as on the page (values unescaped).
    pub labels: Vec<(String, String)>,
    /// The value.
    pub value: f64,
    /// Whether it counts up (a counter, `_count` or `_sum` of a histogram or summary).
    pub counter: bool,
}

/// What a page held.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Page {
    /// The series worth keeping: no buckets, no `_created`, no NaN or infinity.
    pub series: Vec<Series>,
    /// Lines that didn't parse.
    pub broken: usize,
}

fn valid_name(name: &str) -> bool {
    let mut bytes = name.bytes();
    bytes
        .next()
        .is_some_and(|b| b.is_ascii_alphabetic() || b == b'_' || b == b':')
        && bytes.all(|b| b.is_ascii_alphanumeric() || b == b'_' || b == b':')
}

fn kind(word: &str) -> Kind {
    match word {
        "counter" => Kind::Counter,
        "gauge" => Kind::Gauge,
        "histogram" | "gaugehistogram" => Kind::Histogram,
        "summary" => Kind::Summary,
        _ => Kind::Untyped,
    }
}

/// Parses a page; only the series kept are returned.
#[must_use]
pub fn parse(text: &str) -> Page {
    let mut families: BTreeMap<String, Kind> = BTreeMap::new();
    let mut page = Page::default();
    for line in text.lines() {
        let line = line.trim_end_matches('\r');
        if line.len() > MAX_LINE {
            page.broken += 1;
            continue;
        }
        if let Some(comment) = line.strip_prefix('#') {
            let mut words = comment.split_whitespace();
            match (words.next(), words.next(), words.next()) {
                (Some("EOF"), None, None) => break,
                (Some("TYPE"), Some(family), Some(word))
                    if valid_name(family) && families.len() < MAX_FAMILIES =>
                {
                    families.insert(family.to_owned(), kind(word));
                }
                _ => {}
            }
            continue;
        }
        if line.trim().is_empty() {
            continue;
        }
        let Some((name, labels, value)) = sample(line) else {
            page.broken += 1;
            continue;
        };
        // The creation time of a counter (OpenMetrics, and the text format of newer clients).
        if name.ends_with("_created") {
            continue;
        }
        let (family, suffix) = family_of(&name, &families);
        let counter = match (family, suffix) {
            (Kind::Counter, _) | (Kind::Histogram | Kind::Summary, "_count" | "_sum") => true,
            (Kind::Histogram, _) => continue,
            (Kind::Untyped, _) => name.ends_with("_total"),
            _ => false,
        };
        if !value.is_finite() {
            continue;
        }
        page.series.push(Series {
            name,
            labels,
            value,
            counter,
        });
    }
    page
}

/// The family's type and the suffix the sample adds to the family's name (none if the sample
/// is named like its family, as counters are in the text format).
fn family_of<'a>(name: &'a str, families: &BTreeMap<String, Kind>) -> (Kind, &'a str) {
    if let Some(k) = families.get(name) {
        return (*k, "");
    }
    for suffix in ["_total", "_bucket", "_count", "_sum", "_info"] {
        if let Some(base) = name.strip_suffix(suffix)
            && let Some(k) = families.get(base)
        {
            return (*k, suffix);
        }
    }
    (Kind::Untyped, "")
}

/// Labels of a series: name and value.
type Labels = Vec<(String, String)>;

/// `name{labels} value [timestamp] [# exemplar]`.
fn sample(line: &str) -> Option<(String, Labels, f64)> {
    let end = line
        .find(|c: char| c == '{' || c.is_ascii_whitespace())
        .unwrap_or(line.len());
    let name = &line[..end];
    if !valid_name(name) {
        return None;
    }
    let mut rest = &line[end..];
    let mut labels = Vec::new();
    if let Some(inner) = rest.strip_prefix('{') {
        let (parsed, after) = parse_labels(inner)?;
        labels = parsed;
        rest = after;
    }
    let value = rest.split_whitespace().next()?;
    let value = match value {
        "+Inf" | "Inf" => f64::INFINITY,
        "-Inf" => f64::NEG_INFINITY,
        "NaN" => f64::NAN,
        v if v
            .bytes()
            .all(|b| b.is_ascii_digit() || b"+-.eE".contains(&b)) =>
        {
            v.parse().ok()?
        }
        _ => return None,
    };
    Some((name.to_owned(), labels, value))
}

/// The labels up to the closing brace and what follows it.
fn parse_labels(mut s: &str) -> Option<(Labels, &str)> {
    let mut labels = Vec::new();
    loop {
        s = s.trim_start();
        if let Some(after) = s.strip_prefix('}') {
            return Some((labels, after));
        }
        let eq = s.find('=')?;
        let name = s[..eq].trim();
        if !valid_name(name) || name.contains(':') || labels.len() >= MAX_LABELS {
            return None;
        }
        s = s[eq + 1..].trim_start().strip_prefix('"')?;
        let mut value = String::new();
        let mut chars = s.char_indices();
        let close = loop {
            match chars.next()? {
                (i, '"') => break i,
                (_, '\\') => match chars.next()?.1 {
                    'n' => value.push('\n'),
                    '\\' => value.push('\\'),
                    '"' => value.push('"'),
                    _ => return None,
                },
                // A raw control character: the UI and the protocol take none.
                (_, c) if c.is_control() => return None,
                (_, c) => value.push(c),
            }
        };
        labels.push((name.to_owned(), value));
        s = s[close + 1..].trim_start();
        if let Some(after) = s.strip_prefix(',') {
            s = after;
        } else if !s.starts_with('}') {
            return None;
        }
    }
}

/// The labels as one text, sorted by name and escaped like the format: `code="200",method="GET"`.
#[must_use]
pub fn canonical(labels: &[(String, String)]) -> String {
    let mut sorted: Vec<&(String, String)> = labels.iter().collect();
    sorted.sort();
    sorted
        .iter()
        .map(|(k, v)| {
            let v = v
                .replace('\\', "\\\\")
                .replace('"', "\\\"")
                .replace('\n', "\\n");
            format!("{k}=\"{v}\"")
        })
        .collect::<Vec<_>>()
        .join(",")
}

#[cfg(test)]
mod tests;
