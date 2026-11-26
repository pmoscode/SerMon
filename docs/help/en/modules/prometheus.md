# Prometheus scraper

Takes series from `/metrics` endpoints (Prometheus text format and OpenMetrics), e.g. of an application of your own
or an exporter. The module is off by default.

## How the series arrive

SerMon knows every metric in advance (catalog); foreign names therefore don't become metrics of their own. Every
series lands in `sermon_prometheus_gauge` (values) or `sermon_prometheus_rate` (counters per second), the foreign
name in the label `name`, its labels in the label `series`. So nothing collides with the agent's metrics. Buckets of
histograms and `_created` are left out.

## Requirements

Only endpoints within `module_targets` in `[local]` of `/etc/sermon/agent.toml` (by default loopback). At most 1 MiB
an answer, at most three redirects within the allowed targets.

## Settings

- **Endpoints:** up to 8 URLs.
- **Metrics:** names to take, `prefix*` for all starting so; empty means all up to the limit. Better keep it narrow:
  exporters like the node exporter deliver thousands of series.
- **Series per endpoint:** by default 50, all endpoints together at most 250.
- **Bearer token:** sent as `Authorization: Bearer …` to the configured endpoints only, never after a redirect to
  another host; the agent keeps it in memory only (M5-05). Over `http://` it goes in clear text to whoever listens
  there – while the exporter is stopped also to another local user who takes the port. So with a token use
  `https://` with a verified certificate and a token that may read `/metrics` only.
- **Timeout**, **verify TLS**, **CA file**.

## Metrics

- `sermon_prometheus_up`, `sermon_prometheus_series`, `sermon_prometheus_dropped_series` per endpoint.
- `sermon_prometheus_gauge`, `sermon_prometheus_rate` with `name` and `series`.

## When it doesn't work

- "not the Prometheus text format": something else answers at the URL.
- `sermon_prometheus_dropped_series` above 0: the limit is reached or labels exceed 256 bytes.
