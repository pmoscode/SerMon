# Apache httpd

Workers, connections and traffic of Apache from `server-status?auto` (`mod_status`). The module is off by default;
the agent suggests it when it finds Apache (process `apache2` or `httpd`, the unit, port 80/443).

## Requirements

- **Debian, Ubuntu:** `mod_status` is on and shows `/server-status` to localhost only – nothing to do.
- **Rocky, Alma, Fedora (RHEL family):** `mod_status` is loaded, the page is missing. E.g. in
  `/etc/httpd/conf.d/sermon-status.conf`:

  ```apache
  <Location "/server-status">
      SetHandler server-status
      Require local
  </Location>
  ```

  Then `apachectl configtest` and `systemctl reload httpd`. `Require local` allows requests from the host itself
  only.

Without `?auto` the page also shows the current requests and clients; the module reads the counters only and passes
on no requests, URLs or addresses. The agent only reaches targets within `module_targets` (by default loopback).

## Settings

- **Status URL:** by default `http://127.0.0.1/server-status?auto` – the `?auto` belongs to it.
- **Timeout**, for HTTPS **verify TLS** and a **CA file**.

## Metrics

- `sermon_apache_up`: Apache answers.
- `sermon_apache_workers_busy`, `sermon_apache_workers_idle`, `sermon_apache_workers_max`: with every slot busy,
  requests wait – check `MaxRequestWorkers`.
- `sermon_apache_processes`, `sermon_apache_connections` (not with prefork).
- `sermon_apache_requests_per_second`, `sermon_apache_sent_bytes_per_second`.

## When it doesn't work

- "status 403/404 – is mod_status enabled there, for this host?": the page isn't set up or not allowed for
  localhost.
- "an HTML page: add ?auto to the URL": the URL lacks `?auto`.
