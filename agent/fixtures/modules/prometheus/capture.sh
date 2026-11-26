#!/bin/sh
# Captures /metrics (M5-19) of Prometheus itself (counters, gauges, histograms, summaries) and the
# node exporter (many labels) in the text format 0.0.4 – both answer the OpenMetrics Accept header
# with it too – and of the Python client in both formats, OpenMetrics with `_created`, info and
# stateset series, an exemplar and `# EOF`. Addresses of the Docker network become documentation
# addresses.
# Usage: agent/fixtures/modules/prometheus/capture.sh  (Docker)
set -eu
cd "$(dirname "$0")"
text='text/plain;version=0.0.4'

capture() { # NAME IMAGE PORT [ARGS…]
	name=$1 image=$2 port=$3
	shift 3
	docker rm -f "capture-prom-$name" >/dev/null 2>&1 || true
	docker run -d --name "capture-prom-$name" "$image" "$@" >/dev/null
	sleep 6
	mkdir -p "$name"
	docker run --rm --network "container:capture-prom-$name" curlimages/curl:8.11.1 \
		-s -H "Accept: $text" "http://127.0.0.1:$port/metrics" >"$name/metrics-text.txt"
	sed -i.bak -E 's/(172|10)\.[0-9]+\.[0-9]+\.([0-9]+)/192.0.2.\2/g' "$name/metrics-text.txt"
	rm "$name/metrics-text.txt.bak"
	docker rm -f "capture-prom-$name" >/dev/null
	echo "✓ $image → agent/fixtures/modules/prometheus/$name/"
}

capture prometheus-3 prom/prometheus:v3.5.0 9090
capture node-exporter-1 prom/node-exporter:v1.9.1 9100

# The Python client: OpenMetrics and text from the same registry.
mkdir -p python-client-0
docker run --rm -i python:3.13-slim sh -c 'pip install -q prometheus_client==0.21.1 >/dev/null 2>&1 && python3 -' >python-client-0/both.txt <<'EOF'
from prometheus_client import CollectorRegistry, Counter, Gauge, Histogram, Summary, Info, Enum
from prometheus_client import generate_latest
from prometheus_client.openmetrics.exposition import generate_latest as generate_openmetrics
r = CollectorRegistry()
c = Counter("http_requests", "Requests.", ["method", "code"], registry=r)
c.labels("GET", "200").inc(42)
c.labels("POST", "500").inc(3)
Gauge("queue_depth", "Jobs waiting.", registry=r).set(7)
h = Histogram("request_seconds", "Latency.", buckets=[0.1, 1], registry=r)
h.observe(0.05, {"trace_id": "abc123"})
h.observe(0.5)
Summary("payload_bytes", "Payload.", registry=r).observe(512)
Info("build", "Build.", registry=r).info({"version": "1.2.3"})
Enum("state", "State.", states=["up", "down"], registry=r).state("up")
Gauge("odd_values", "Specials.", ["kind"], registry=r).labels('quote"back\\slash\nline').set(float("nan"))
print(generate_openmetrics(r).decode(), end="")
print("=====")
print(generate_latest(r).decode(), end="")
EOF
awk 'BEGIN{f="python-client-0/metrics-openmetrics.txt"} /^=====$/{f="python-client-0/metrics-text.txt"; next} {print > f}' python-client-0/both.txt
rm python-client-0/both.txt
echo "✓ prometheus_client → agent/fixtures/modules/prometheus/python-client-0/"
