# HTTP check

Checks HTTP and HTTPS endpoints: whether they answer, with which status code, how fast and how long their certificate
has left. The module is off by default; up to 16 URLs are asked at once, the body of the answer is not read.

## Requirements

The agent only reaches targets the host allows: `module_targets` in the `[local]` section of
`/etc/sermon/agent.toml`, by default loopback only (`127.0.0.0/8`, `::1`). To check a service in the network, add its
network there. A name is resolved once and every address must be allowed.

## Settings

- **URLs:** `http://` or `https://`, without user and password. The query (`?…`) is sent but is not part of the
  metrics' label.
- **Expected status codes:** empty means 200 to 399.
- **Timeout:** per request with all its redirects, at most 25 s.
- **Follow redirects:** at most three, only to allowed targets, never from HTTPS to HTTP.
- **Verify TLS certificates:** on by default. For a self-signed certificate rather give a **CA file** (below
  `module_file_prefixes`, by default `/etc/ssl`, `/etc/pki`, `/etc/letsencrypt`) than turning verification off.

## Metrics

- `sermon_http_check_up`: 1 for an answer with an expected status.
- `sermon_http_check_status_code`, `sermon_http_check_duration_seconds` (until the headers of the last answer).
- `sermon_http_check_cert_expires_in_seconds`: time left of the configured host's certificate.

## When it doesn't work

- "… is not in module_targets of agent.toml" (state `vetoed`): the target or a redirect lies outside the allowed
  networks.
- `up` 0 without a status code: no answer (connection refused, timeout, TLS error).
