use super::*;

const NGINX: &str = r#"{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "nginx",
  "type": "object",
  "additionalProperties": false,
  "required": ["url"],
  "properties": {
    "url": {"type": "string", "format": "uri", "maxLength": 512, "default": "http://127.0.0.1/stub_status"},
    "timeout": {"type": "string", "format": "duration", "maxLength": 8, "default": "5s"},
    "port": {"type": "integer", "minimum": 1, "maximum": 65535},
    "ratio": {"type": "number", "minimum": 0, "maximum": 1},
    "verify": {"type": "boolean", "default": true},
    "mode": {"type": "string", "enum": ["plain", "tls"], "maxLength": 8},
    "user": {"type": "string", "pattern": "^[a-z_][a-z0-9_]*$", "maxLength": 63},
    "password": {"type": "string", "writeOnly": true, "maxLength": 256},
    "paths": {"type": "array", "maxItems": 4, "items": {"type": "string", "format": "path", "maxLength": 256}},
    "codes": {"type": "array", "maxItems": 3, "items": {"type": "integer", "minimum": 100, "maximum": 599}}
  }
}"#;

fn schema() -> Schema {
    Schema::parse(NGINX).unwrap()
}

#[test]
fn a_schema_of_the_subset_parses_and_names_its_credentials() {
    let s = schema();
    assert_eq!(s.secrets().collect::<Vec<_>>(), ["password"]);
}

#[test]
fn a_schema_outside_the_subset_is_refused() {
    let with = |old: &str, new: &str| {
        assert!(NGINX.contains(old), "{old}");
        Schema::parse(&NGINX.replacen(old, new, 1))
    };
    for (old, new, why) in [
        (
            r#""additionalProperties": false"#,
            r#""additionalProperties": true"#,
            "additionalProperties",
        ),
        (r#""type": "object""#, r#""type": "array""#, "type object"),
        (r#", "maxLength": 512"#, "", "maxLength"),
        (r#""minimum": 1, "#, "", "minimum"),
        (r#""maxItems": 4, "#, "", "maxItems"),
        (r#""^[a-z_][a-z0-9_]*$""#, r#""[a-z]+""#, "anchored"),
        (r#""format": "uri""#, r#""format": "email""#, "format"),
        (
            r#""writeOnly": true"#,
            r#""writeOnly": true, "default": "x""#,
            "no default",
        ),
        (r#""default": "5s""#, r#""default": "2h""#, "default"),
        (
            r#""required": ["url"]"#,
            r#""required": ["nope"]"#,
            "unknown property",
        ),
        (
            r#""verify": {"type": "boolean""#,
            r#""verify": {"type": "boolean", "oneOf": []"#,
            "subset",
        ),
        (r#""port": {"#, r#""Port": {"#, "property name"),
        (
            r#""type": "integer", "minimum": 1"#,
            r#""type": "integer", "writeOnly": true, "minimum": 1"#,
            "credential",
        ),
    ] {
        let r = with(old, new);
        assert!(r.as_ref().is_err_and(|e| e.contains(why)), "{why}: {r:?}");
    }
}

#[test]
fn a_configuration_gets_its_defaults() {
    let c = schema()
        .validate(r#"{"url":"http://127.0.0.1:8080/status"}"#, &[])
        .unwrap();
    assert_eq!(c["url"], "http://127.0.0.1:8080/status");
    assert_eq!(c["timeout"], "5s");
    assert_eq!(c["verify"], true);
    assert!(!c.contains_key("port"));
    // An empty configuration takes the defaults, url included.
    assert_eq!(schema().validate("", &[]).unwrap_err(), "url: required");
}

#[test]
fn a_configuration_outside_its_schema_is_refused() {
    let s = schema();
    for (json, why) in [
        (r#"{"url":"ftp://x/"}"#, "url: an http or https URL"),
        (
            r#"{"url":"http://alice@127.0.0.1/"}"#,
            "without user and password",
        ),
        (
            r#"{"url":"http://x/","extra":1}"#,
            "extra: unknown property",
        ),
        (
            r#"{"url":"http://x/","port":0}"#,
            "port: 0 is not within 1 to 65535",
        ),
        (r#"{"url":"http://x/","port":"80"}"#, "port: not an integer"),
        (r#"{"url":"http://x/","ratio":1.5}"#, "ratio:"),
        (r#"{"url":"http://x/","mode":"udp"}"#, "mode:"),
        (
            r#"{"url":"http://x/","user":"Root"}"#,
            "user: doesn't match",
        ),
        (
            r#"{"url":"http://x/","user":"a\u0007"}"#,
            "control characters",
        ),
        (
            r#"{"url":"http://x/","password":"pw"}"#,
            "doesn't belong in the configuration",
        ),
        (
            r#"{"url":"http://x/","paths":["/etc/../shadow"]}"#,
            "paths: item 1",
        ),
        (
            r#"{"url":"http://x/","paths":["/a","/b","/c","/d","/e"]}"#,
            "more than 4 items",
        ),
        (r#"{"url":"http://x/","codes":[200,700]}"#, "codes: item 2"),
        (r#"{"url":"http://x/","timeout":"90m"}"#, "at most an hour"),
        ("[1]", "no object"),
        (r#"{"url":"#, "not JSON"),
    ] {
        let r = s.validate(json, &[]);
        assert!(r.as_ref().is_err_and(|e| e.contains(why)), "{json}: {r:?}");
    }
}

#[test]
fn a_required_credential_comes_from_the_secrets() {
    let s = Schema::parse(&NGINX.replacen(
        r#""required": ["url"]"#,
        r#""required": ["url", "password"]"#,
        1,
    ))
    .unwrap();
    let json = r#"{"url":"http://x/"}"#;
    assert_eq!(s.validate(json, &[]).unwrap_err(), "password: required");
    assert!(s.validate(json, &["password"]).is_ok());
}

#[test]
fn host_port_and_duration() {
    assert_eq!(host_port("127.0.0.1:5432"), Ok(("127.0.0.1", 5432)));
    assert_eq!(host_port("[::1]:6379"), Ok(("::1", 6379)));
    assert_eq!(host_port("db.local:3306"), Ok(("db.local", 3306)));
    for bad in ["127.0.0.1", "x:0", "x:65536", ":80", "a b:80", "[::1]6379"] {
        assert!(host_port(bad).is_err(), "{bad}");
    }
    assert_eq!(duration("500ms"), Ok(Duration::from_millis(500)));
    assert_eq!(duration("2m"), Ok(Duration::from_secs(120)));
    for bad in ["5", "s", "1d", "61m", "-1s"] {
        assert!(duration(bad).is_err(), "{bad}");
    }
}

proptest::proptest! {
    // Whatever the server sends, checking it doesn't panic.
    #[test]
    fn validating_anything_doesnt_panic(json in ".{0,200}") {
        let _ = schema().validate(&json, &["password"]);
    }
}
