//! The redaction cases the server's Go port must give the same way (`testdata/redaction/`,
//! M3-12): the test of rules in the UI shows what the agent does.

// Test code: panicking on setup failures is the desired behaviour.
#![allow(clippy::unwrap_used, clippy::expect_used)]

use std::path::PathBuf;

use sermon_security::{Redactor, RuleSpec};

#[test]
fn the_shared_cases_hold() {
    let path =
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../../testdata/redaction/cases.json");
    let doc: serde_json::Value =
        serde_json::from_str(&std::fs::read_to_string(path).unwrap()).unwrap();
    let cases = doc["cases"].as_array().unwrap();
    assert!(cases.len() > 30);
    let plain = |v: &serde_json::Value| v.as_str().unwrap().replace("{{}}", "");
    for case in cases {
        let rules: Vec<RuleSpec> = case["rules"]
            .as_array()
            .unwrap()
            .iter()
            .map(|r| RuleSpec { pattern: plain(r) })
            .collect();
        let redactor = Redactor::with_rules(&rules).unwrap();
        assert_eq!(
            redactor.redact(&plain(&case["input"])),
            plain(&case["output"]),
            "{}",
            case["name"]
        );
    }
}
