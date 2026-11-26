use std::path::Path;

use super::*;

fn fixture(source: &str, format: &str) -> String {
    std::fs::read_to_string(
        Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../../fixtures/modules/prometheus")
            .join(source)
            .join(format!("metrics-{format}.txt")),
    )
    .unwrap()
}

fn find<'a>(page: &'a Page, name: &str, labels: &str) -> Option<&'a Series> {
    page.series
        .iter()
        .find(|s| s.name == name && canonical(&s.labels) == labels)
}

#[test]
fn reads_the_exporters_pages() {
    let prom = parse(&fixture("prometheus-3", "text"));
    assert_eq!(prom.broken, 0);
    let requests = find(
        &prom,
        "prometheus_http_requests_total",
        r#"code="200",handler="/""#,
    )
    .unwrap();
    assert!(requests.counter);
    let quantile = find(&prom, "go_gc_duration_seconds", r#"quantile="0.5""#).unwrap();
    assert!(!quantile.counter, "a quantile is a gauge");
    assert!(
        find(&prom, "go_gc_duration_seconds_count", "")
            .unwrap()
            .counter
    );
    assert!(
        !prom.series.iter().any(|s| s.name.ends_with("_bucket")),
        "no buckets"
    );
    assert!(
        find(&prom, "prometheus_tsdb_head_series_created_total", "")
            .unwrap()
            .counter,
        "_created_total is a counter of its own"
    );
    let gauge = find(&prom, "go_goroutines", "").unwrap();
    assert!(!gauge.counter && gauge.value > 0.0);

    let node = parse(&fixture("node-exporter-1", "text"));
    assert_eq!(node.broken, 0);
    assert!(node.series.len() > 1000, "{}", node.series.len());
    assert!(
        node.series
            .iter()
            .any(|s| s.name == "node_network_info" && s.labels.len() == 7)
    );
}

// The same registry in both formats gives the same series (but the text format's
// `_created` gauges, which both leave out).
#[test]
fn reads_openmetrics_like_the_text_format() {
    let om = parse(&fixture("python-client-0", "openmetrics"));
    let text = parse(&fixture("python-client-0", "text"));
    assert_eq!(om.broken, 0);
    assert_eq!(text.broken, 0);
    for page in [&om, &text] {
        let get = find(page, "http_requests_total", r#"code="200",method="GET""#).unwrap();
        assert!(get.counter && (get.value - 42.0).abs() < f64::EPSILON);
        assert!(!find(page, "queue_depth", "").unwrap().counter);
        assert!(find(page, "request_seconds_count", "").unwrap().counter);
        assert!(find(page, "request_seconds_sum", "").unwrap().counter);
        assert!(find(page, "payload_bytes_sum", "").unwrap().counter);
        assert!(
            !find(page, "build_info", r#"version="1.2.3""#)
                .unwrap()
                .counter
        );
        assert!(find(page, "state", r#"state="up""#).is_some());
        assert!(
            !page.series.iter().any(|s| s.name.ends_with("_created")
                || s.name.ends_with("_bucket")
                || s.name == "odd_values"),
            "no _created, buckets or NaN"
        );
    }
    assert_eq!(om.series.len(), text.series.len());
}

#[test]
fn unescapes_and_escapes_labels() {
    let page = parse("x{a=\"q\\\"b\\\\s\\nl\",b=\"\"} 1\n");
    assert_eq!(page.series[0].labels[0].1, "q\"b\\s\nl");
    assert_eq!(canonical(&page.series[0].labels), r#"a="q\"b\\s\nl",b="""#);
    let trailing = parse("x{b=\"2\",a=\"1\",} 3 1700000000000\n");
    assert_eq!(
        canonical(&trailing.series[0].labels),
        r#"a="1",b="2""#,
        "sorted"
    );
}

// Broken lines are skipped and counted; the rest of the page counts.
#[test]
fn skips_broken_lines() {
    let lines = [
        "1abc 1",
        "x{a=1} 1",
        "x{a=\"1\" 1",
        "x{a=\"\\q\"} 1",
        "x{a:b=\"1\"} 1",
        "x one",
        "x",
        "x{} 0x10",
        "x{a=\"t\tab\"} 1",
        "ok 1",
        "inf +Inf",
        "nan NaN",
    ];
    let page = parse(&lines.join("\n"));
    assert_eq!(page.broken, 9);
    assert_eq!(page.series.len(), 1);
    assert_eq!(page.series[0].name, "ok");
    let many = (0..=MAX_LABELS)
        .map(|i| format!("l{i}=\"v\""))
        .collect::<Vec<_>>()
        .join(",");
    assert_eq!(
        parse(&format!("x{{{many}}} 1")).broken,
        1,
        "too many labels"
    );
    assert_eq!(
        parse(&format!("x{{a=\"{}\"}} 1", "v".repeat(MAX_LINE))).broken,
        1
    );
    let eof = parse("a 1\n# EOF\nb 2\n");
    assert_eq!(eof.series.len(), 1, "# EOF ends the page");
}

#[test]
fn an_untyped_total_counts_up() {
    let page = parse("jobs_total 5\njobs 3\n");
    assert!(page.series[0].counter);
    assert!(!page.series[1].counter);
}
