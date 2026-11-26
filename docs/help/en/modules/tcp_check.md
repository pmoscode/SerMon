# TCP check

Checks whether TCP ports accept connections and how fast. The module is off by default; up to 16 targets are checked at
once, nothing is sent.

## Requirements

Only targets within `module_targets` in `[local]` of `/etc/sermon/agent.toml` (by default loopback). A name is
resolved once and every address must be allowed.

## Settings

- **Targets:** `host:port`, `[address]:port` for IPv6.
- **Timeout:** per connection, at most 25 s.

## Metrics

- `sermon_tcp_check_up`: 1 if a connection was made.
- `sermon_tcp_check_duration_seconds`: time until the connection stood.

## When it doesn't work

- State `vetoed`: the target lies outside `module_targets`.
