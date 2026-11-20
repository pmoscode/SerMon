//! Every metric name the agent can send is in the server's catalog
//! (`server/internal/domain/metric/catalog.go`): the server drops unknown metrics (M1-25).

// Test code: panicking on setup failures is the desired behaviour.
#![allow(clippy::unwrap_used, clippy::expect_used)]

use std::collections::BTreeSet;
use std::path::{Path, PathBuf};

fn names_in(text: &str, prefix: &str) -> BTreeSet<String> {
    let mut names = BTreeSet::new();
    let mut rest = text;
    while let Some(start) = rest.find(prefix) {
        let candidate = &rest[start + prefix.len() - "sermon_".len()..];
        let name: String = candidate
            .chars()
            .take_while(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || *c == '_')
            .collect();
        // Skip prefixes of composed names in docs (e.g. "sermon_<area>_…").
        if name.len() > "sermon_".len() && candidate[name.len()..].starts_with('"') {
            names.insert(name);
        }
        rest = &rest[start + prefix.len()..];
    }
    names
}

fn sources(dir: &Path) -> Vec<PathBuf> {
    std::fs::read_dir(dir)
        .unwrap()
        .map(|entry| entry.unwrap().path())
        .filter(|path| path.extension().is_some_and(|ext| ext == "rs"))
        .collect()
}

#[test]
fn every_collected_metric_is_in_the_server_catalog() {
    let root = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../..");
    let catalog =
        std::fs::read_to_string(root.join("server/internal/domain/metric/catalog.go")).unwrap();
    let known = names_in(&catalog, "def(\"sermon_");
    assert!(known.len() > 70, "catalog not parsed: {known:?}");

    let mut files = sources(&root.join("agent/crates/sermon-collectors/src"));
    files.push(root.join("agent/crates/sermon-agent/src/scheduler.rs"));
    files.push(root.join("agent/crates/sermon-agent/src/simulate.rs"));
    let mut sent = BTreeSet::new();
    for file in &files {
        let text = std::fs::read_to_string(file).unwrap();
        // Only production code: the test modules use made-up names.
        let production = text.split("#[cfg(test)]").next().unwrap_or_default();
        sent.extend(names_in(production, "\"sermon_"));
    }
    assert!(sent.len() > 70, "no metric names found: {sent:?}");

    let missing: Vec<&String> = sent.difference(&known).collect();
    assert!(
        missing.is_empty(),
        "missing in the server catalog: {missing:?}"
    );
}
