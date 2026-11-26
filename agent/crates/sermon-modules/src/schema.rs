//! The JSON schema of a module's configuration (ADR 0039): a fixed subset of JSON Schema
//! 2020-12 the UI builds a form from and the agent checks every configuration against. A schema
//! outside the subset is a bug of the module; a configuration outside its schema is the server's
//! error and keeps the module off.
//!
//! - The root is an object with `properties` and `additionalProperties: false`.
//! - Values: `string` (`maxLength` required; `minLength`, `pattern` anchored with `^…$`, `enum`,
//!   `format` of `uri`, `host-port`, `path`, `duration`), `integer` and `number` (`minimum` and
//!   `maximum` required), `boolean`, `array` of `string` or `integer` (`maxItems` required).
//! - `title`, `description`, `default`; `writeOnly: true` marks a credential, which never comes
//!   in the configuration but in the module's secrets.

use std::collections::BTreeMap;
use std::time::Duration;

use regex::{Regex, RegexBuilder};
use serde_json::{Map, Value};

/// Longest property name (also the `field` of a secret in the protocol).
const MAX_NAME: usize = 64;
/// Most properties of a schema.
const MAX_PROPERTIES: usize = 64;
/// Size of a compiled pattern.
const PATTERN_SIZE: usize = 64 * 1024;
/// Longest duration a `duration` value may name.
const MAX_DURATION: Duration = Duration::from_hours(1);

/// A checked schema.
#[derive(Debug, Clone)]
pub struct Schema {
    properties: BTreeMap<String, Property>,
    required: Vec<String>,
}

#[derive(Debug, Clone)]
struct Property {
    kind: Kind,
    default: Option<Value>,
    secret: bool,
}

#[derive(Debug, Clone)]
enum Kind {
    String(Text),
    Integer {
        min: i64,
        max: i64,
    },
    Number {
        min: f64,
        max: f64,
    },
    Boolean,
    Strings {
        max_items: usize,
        item: Text,
    },
    Integers {
        max_items: usize,
        min: i64,
        max: i64,
    },
}

#[derive(Debug, Clone)]
struct Text {
    min_len: usize,
    max_len: usize,
    pattern: Option<Regex>,
    values: Option<Vec<String>>,
    format: Option<Format>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Format {
    Uri,
    HostPort,
    Path,
    Duration,
}

/// A configuration that follows its schema, with the defaults filled in.
pub type Config = Map<String, Value>;

impl Schema {
    /// Checks `text` against the subset.
    ///
    /// # Errors
    ///
    /// What leaves the subset, naming the property.
    pub fn parse(text: &str) -> Result<Self, String> {
        let root: Value = serde_json::from_str(text).map_err(|e| format!("not JSON: {e}"))?;
        let root = root.as_object().ok_or("the schema is no object")?;
        only(
            root,
            &[
                "$schema",
                "$id",
                "title",
                "description",
                "type",
                "properties",
                "required",
                "additionalProperties",
            ],
            "the schema",
        )?;
        if root.get("type") != Some(&Value::from("object")) {
            return Err("the schema must be of type object".into());
        }
        if root.get("additionalProperties") != Some(&Value::Bool(false)) {
            return Err("the schema must set additionalProperties to false".into());
        }
        let raw = root
            .get("properties")
            .and_then(Value::as_object)
            .ok_or("the schema has no properties")?;
        if raw.len() > MAX_PROPERTIES {
            return Err(format!("more than {MAX_PROPERTIES} properties"));
        }
        let mut properties = BTreeMap::new();
        for (name, p) in raw {
            if !valid_name(name) {
                return Err(format!("{name}: not a property name"));
            }
            let property = property(p).map_err(|e| format!("{name}: {e}"))?;
            if let Some(d) = &property.default {
                if property.secret {
                    return Err(format!("{name}: a credential has no default"));
                }
                check(&property.kind, d).map_err(|e| format!("{name}: default: {e}"))?;
            }
            properties.insert(name.clone(), property);
        }
        let required = match root.get("required") {
            None => Vec::new(),
            Some(r) => r
                .as_array()
                .ok_or("required is no list")?
                .iter()
                .map(|v| {
                    v.as_str()
                        .filter(|n| properties.contains_key(*n))
                        .map(str::to_owned)
                        .ok_or_else(|| format!("required names an unknown property: {v}"))
                })
                .collect::<Result<_, _>>()?,
        };
        Ok(Self {
            properties,
            required,
        })
    }

    /// The properties marked `writeOnly`: the module's credentials.
    pub fn secrets(&self) -> impl Iterator<Item = &str> {
        self.properties
            .iter()
            .filter(|(_, p)| p.secret)
            .map(|(n, _)| n.as_str())
    }

    /// Checks `json` and fills in the defaults; `secrets` are the names of the credentials the
    /// module has (a required one must be among them).
    ///
    /// # Errors
    ///
    /// The first problem, naming the property.
    pub fn validate(&self, json: &str, secrets: &[&str]) -> Result<Config, String> {
        let value: Value = if json.trim().is_empty() {
            Value::Object(Map::new())
        } else {
            serde_json::from_str(json).map_err(|e| format!("not JSON: {e}"))?
        };
        let Value::Object(mut config) = value else {
            return Err("the configuration is no object".into());
        };
        for (name, v) in &config {
            let p = self
                .properties
                .get(name)
                .ok_or_else(|| format!("{name}: unknown property"))?;
            if p.secret {
                return Err(format!(
                    "{name}: a credential doesn't belong in the configuration"
                ));
            }
            check(&p.kind, v).map_err(|e| format!("{name}: {e}"))?;
        }
        for name in &self.required {
            let given = if self.properties[name].secret {
                secrets.contains(&name.as_str())
            } else {
                config.contains_key(name)
            };
            if !given {
                return Err(format!("{name}: required"));
            }
        }
        for (name, p) in &self.properties {
            if let Some(d) = &p.default {
                config.entry(name.clone()).or_insert_with(|| d.clone());
            }
        }
        Ok(config)
    }
}

fn valid_name(name: &str) -> bool {
    let mut bytes = name.bytes();
    name.len() <= MAX_NAME
        && bytes.next().is_some_and(|b| b.is_ascii_lowercase())
        && bytes.all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'_')
}

fn only(object: &Map<String, Value>, keys: &[&str], what: &str) -> Result<(), String> {
    match object.keys().find(|k| !keys.contains(&k.as_str())) {
        Some(k) => Err(format!("{what}: {k} is not in the subset")),
        None => Ok(()),
    }
}

const COMMON: [&str; 5] = ["type", "title", "description", "default", "writeOnly"];

fn property(p: &Value) -> Result<Property, String> {
    let p = p.as_object().ok_or("no object")?;
    let secret = match p.get("writeOnly") {
        None => false,
        Some(Value::Bool(b)) => *b,
        Some(_) => return Err("writeOnly is no boolean".into()),
    };
    let kind = match p.get("type").and_then(Value::as_str) {
        Some("string") => {
            only(
                p,
                &[
                    &COMMON[..],
                    &["maxLength", "minLength", "pattern", "enum", "format"],
                ]
                .concat(),
                "a string",
            )?;
            Kind::String(text(p)?)
        }
        Some("integer") => {
            only(
                p,
                &[&COMMON[..], &["minimum", "maximum"]].concat(),
                "an integer",
            )?;
            let (min, max) = int_bounds(p)?;
            Kind::Integer { min, max }
        }
        Some("number") => {
            only(
                p,
                &[&COMMON[..], &["minimum", "maximum"]].concat(),
                "a number",
            )?;
            let bound = |k: &str| {
                p.get(k)
                    .and_then(Value::as_f64)
                    .ok_or_else(|| format!("a number needs {k}"))
            };
            let (min, max) = (bound("minimum")?, bound("maximum")?);
            if min > max {
                return Err("minimum above maximum".into());
            }
            Kind::Number { min, max }
        }
        Some("boolean") => {
            only(p, &COMMON, "a boolean")?;
            Kind::Boolean
        }
        Some("array") => {
            only(
                p,
                &[&COMMON[..], &["maxItems", "items"]].concat(),
                "an array",
            )?;
            array(p)?
        }
        _ => return Err("type is string, integer, number, boolean or array".into()),
    };
    if secret && !matches!(kind, Kind::String(_)) {
        return Err("only a string can be a credential".into());
    }
    Ok(Property {
        kind,
        default: p.get("default").cloned(),
        secret,
    })
}

fn array(p: &Map<String, Value>) -> Result<Kind, String> {
    let max_items = p
        .get("maxItems")
        .and_then(Value::as_u64)
        .and_then(|n| usize::try_from(n).ok())
        .filter(|n| (1..=256).contains(n))
        .ok_or("an array needs maxItems of 1 to 256")?;
    let items = p
        .get("items")
        .and_then(Value::as_object)
        .ok_or("an array needs items")?;
    match items.get("type").and_then(Value::as_str) {
        Some("string") => {
            only(
                items,
                &[
                    "type",
                    "maxLength",
                    "minLength",
                    "pattern",
                    "enum",
                    "format",
                ],
                "items",
            )?;
            Ok(Kind::Strings {
                max_items,
                item: text(items)?,
            })
        }
        Some("integer") => {
            only(items, &["type", "minimum", "maximum"], "items")?;
            let (min, max) = int_bounds(items)?;
            Ok(Kind::Integers {
                max_items,
                min,
                max,
            })
        }
        _ => Err("items are strings or integers".into()),
    }
}

fn int_bounds(p: &Map<String, Value>) -> Result<(i64, i64), String> {
    let bound = |k: &str| {
        p.get(k)
            .and_then(Value::as_i64)
            .ok_or_else(|| format!("an integer needs {k}"))
    };
    let (min, max) = (bound("minimum")?, bound("maximum")?);
    if min > max {
        return Err("minimum above maximum".into());
    }
    Ok((min, max))
}

fn text(p: &Map<String, Value>) -> Result<Text, String> {
    let len = |k: &str| {
        p.get(k)
            .map(|v| {
                v.as_u64()
                    .and_then(|n| usize::try_from(n).ok())
                    .ok_or_else(|| format!("{k} is no count"))
            })
            .transpose()
    };
    let max_len = len("maxLength")?
        .filter(|n| (1..=4096).contains(n))
        .ok_or("a string needs maxLength of 1 to 4096")?;
    let min_len = len("minLength")?.unwrap_or(0);
    if min_len > max_len {
        return Err("minLength above maxLength".into());
    }
    let pattern = match p.get("pattern") {
        None => None,
        Some(Value::String(s)) if s.starts_with('^') && s.ends_with('$') => Some(
            RegexBuilder::new(s)
                .size_limit(PATTERN_SIZE)
                .dfa_size_limit(PATTERN_SIZE)
                .build()
                .map_err(|e| format!("pattern: {e}"))?,
        ),
        Some(_) => return Err("a pattern is anchored with ^ and $".into()),
    };
    let values = match p.get("enum") {
        None => None,
        Some(Value::Array(a)) if !a.is_empty() => Some(
            a.iter()
                .map(|v| v.as_str().map(str::to_owned).ok_or("enum holds strings"))
                .collect::<Result<_, _>>()?,
        ),
        Some(_) => return Err("enum is a list of strings".into()),
    };
    let format = match p.get("format").and_then(Value::as_str) {
        None => None,
        Some("uri") => Some(Format::Uri),
        Some("host-port") => Some(Format::HostPort),
        Some("path") => Some(Format::Path),
        Some("duration") => Some(Format::Duration),
        Some(f) => return Err(format!("format {f} is not in the subset")),
    };
    Ok(Text {
        min_len,
        max_len,
        pattern,
        values,
        format,
    })
}

fn check(kind: &Kind, v: &Value) -> Result<(), String> {
    match kind {
        Kind::String(t) => check_text(t, v.as_str().ok_or("not a string")?),
        Kind::Integer { min, max } => {
            let n = v.as_i64().ok_or("not an integer")?;
            if n < *min || n > *max {
                return Err(format!("{n} is not within {min} to {max}"));
            }
            Ok(())
        }
        Kind::Number { min, max } => {
            let n = v.as_f64().ok_or("not a number")?;
            if !n.is_finite() || n < *min || n > *max {
                return Err(format!("{n} is not within {min} to {max}"));
            }
            Ok(())
        }
        Kind::Boolean => v
            .as_bool()
            .map(|_| ())
            .ok_or_else(|| "not a boolean".into()),
        Kind::Strings { max_items, item } => {
            let items = v.as_array().ok_or("not a list")?;
            if items.len() > *max_items {
                return Err(format!("more than {max_items} items"));
            }
            for (i, item_value) in items.iter().enumerate() {
                let s = item_value.as_str().ok_or("not a string")?;
                check_text(item, s).map_err(|e| format!("item {}: {e}", i + 1))?;
            }
            Ok(())
        }
        Kind::Integers {
            max_items,
            min,
            max,
        } => {
            let items = v.as_array().ok_or("not a list")?;
            if items.len() > *max_items {
                return Err(format!("more than {max_items} items"));
            }
            for (i, item_value) in items.iter().enumerate() {
                let n = item_value.as_i64().ok_or("not an integer")?;
                if n < *min || n > *max {
                    return Err(format!("item {}: {n} is not within {min} to {max}", i + 1));
                }
            }
            Ok(())
        }
    }
}

fn check_text(t: &Text, s: &str) -> Result<(), String> {
    let n = s.chars().count();
    if n < t.min_len || n > t.max_len {
        return Err(format!(
            "length {n} is not within {} to {}",
            t.min_len, t.max_len
        ));
    }
    if s.chars().any(char::is_control) {
        return Err("contains control characters".into());
    }
    if let Some(values) = &t.values
        && !values.iter().any(|v| v == s)
    {
        return Err(format!("{s:?} is not one of {}", values.join(", ")));
    }
    if let Some(p) = &t.pattern
        && !p.is_match(s)
    {
        return Err("doesn't match the pattern".into());
    }
    match t.format {
        None => Ok(()),
        Some(Format::Uri) => uri(s),
        Some(Format::HostPort) => host_port(s).map(|_| ()),
        Some(Format::Path) => path(s),
        Some(Format::Duration) => duration(s).map(|_| ()),
    }
}

fn uri(s: &str) -> Result<(), String> {
    let rest = s
        .strip_prefix("http://")
        .or_else(|| s.strip_prefix("https://"))
        .ok_or("an http or https URL")?;
    let authority = rest.split(['/', '?', '#']).next().unwrap_or_default();
    if authority.contains('@') {
        return Err("a URL without user and password".into());
    }
    if authority.is_empty() || s.contains(' ') {
        return Err("not a URL".into());
    }
    Ok(())
}

/// The host and port of `host:port` (`[::1]:5432` for IPv6).
///
/// # Errors
///
/// If `s` is no host and port.
pub fn host_port(s: &str) -> Result<(&str, u16), String> {
    let (host, port) = if let Some(rest) = s.strip_prefix('[') {
        let (host, port) = rest.split_once("]:").ok_or("not [address]:port")?;
        (host, port)
    } else {
        s.rsplit_once(':').ok_or("not host:port")?
    };
    let port: u16 = port
        .parse()
        .ok()
        .filter(|p| *p > 0)
        .ok_or("the port is 1 to 65535")?;
    if host.is_empty()
        || !host
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b".-:".contains(&b))
    {
        return Err("not a host".into());
    }
    Ok((host, port))
}

fn path(s: &str) -> Result<(), String> {
    if !s.starts_with('/') || s.split('/').any(|seg| seg == "..") || s.contains('\0') {
        return Err("an absolute path without .. segments".into());
    }
    Ok(())
}

/// A duration like `500ms`, `5s`, `2m` or `1h`, at most an hour.
///
/// # Errors
///
/// If `s` is no such duration.
pub fn duration(s: &str) -> Result<Duration, String> {
    let split = s
        .find(|c: char| !c.is_ascii_digit())
        .ok_or("a number and a unit (ms, s, m, h)")?;
    let n: u64 = s[..split].parse().map_err(|_| "not a number")?;
    let d = match &s[split..] {
        "ms" => Duration::from_millis(n),
        "s" => Duration::from_secs(n),
        "m" => Duration::from_secs(n.saturating_mul(60)),
        "h" => Duration::from_secs(n.saturating_mul(3600)),
        _ => return Err("the unit is ms, s, m or h".into()),
    };
    if d > MAX_DURATION {
        return Err("at most an hour".into());
    }
    Ok(d)
}

#[cfg(test)]
mod tests;
