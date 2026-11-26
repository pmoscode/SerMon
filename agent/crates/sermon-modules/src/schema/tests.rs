use super::*;

// The cases both checkers share (the server's runs the same file, ADR 0039).
const VECTORS: &str = include_str!("../../testdata/schema-vectors.json");

fn vectors() -> Value {
    serde_json::from_str(VECTORS).unwrap()
}

fn schema_text() -> String {
    vectors()["schema"].to_string()
}

fn schema() -> Schema {
    Schema::parse(&schema_text()).unwrap()
}

#[test]
fn a_schema_of_the_subset_parses_and_names_its_credentials() {
    let s = schema();
    assert_eq!(s.secrets().collect::<Vec<_>>(), ["password"]);
}

// Sets (or removes) the value at a JSON pointer.
fn patch(root: &mut Value, pointer: &str, value: Option<&Value>) {
    let (parent, key) = pointer.rsplit_once('/').unwrap();
    let parent = root.pointer_mut(parent).unwrap().as_object_mut().unwrap();
    match value {
        Some(v) => parent.insert(key.to_owned(), v.clone()),
        None => parent.remove(key),
    };
}

#[test]
fn the_shared_schema_cases() {
    let vectors = vectors();
    for case in vectors["schema_cases"].as_array().unwrap() {
        let mut schema = vectors["schema"].clone();
        let pointer = case["pointer"].as_str().unwrap();
        let remove = case.get("remove").is_some_and(|r| r == true);
        patch(&mut schema, pointer, (!remove).then(|| &case["value"]));
        let r = Schema::parse(&schema.to_string());
        match case.get("error").and_then(Value::as_str) {
            Some(why) => assert!(r.as_ref().is_err_and(|e| e.contains(why)), "{case}: {r:?}"),
            None => assert!(r.is_ok(), "{case}: {r:?}"),
        }
    }
}

#[test]
fn the_shared_configuration_cases() {
    let vectors = vectors();
    for case in vectors["config_cases"].as_array().unwrap() {
        let mut text = vectors["schema"].clone();
        if let Some(required) = case.get("required") {
            patch(&mut text, "/required", Some(required));
        }
        let schema = Schema::parse(&text.to_string()).unwrap();
        let secrets: Vec<&str> = case
            .get("secrets")
            .and_then(Value::as_array)
            .map_or_else(Vec::new, |a| a.iter().filter_map(Value::as_str).collect());
        let r = schema.validate(case["config"].as_str().unwrap(), &secrets);
        match case.get("error").and_then(Value::as_str) {
            Some(why) => assert!(r.as_ref().is_err_and(|e| e.contains(why)), "{case}: {r:?}"),
            None => assert_eq!(Value::Object(r.unwrap()), case["result"], "{case}"),
        }
    }
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
