# nginx

Connections and requests of nginx from its status page (`stub_status`). The module is off by default; the agent
suggests it when it finds nginx (process, unit or port 80/443).

## Requirements

nginx has to show the status page, only to the agent on the same host – e.g. in
`/etc/nginx/conf.d/sermon-status.conf`:

```nginx
server {
    listen 127.0.0.1:80;
    location = /nginx_status {
        stub_status;
        allow 127.0.0.1;
        deny all;
    }
}
```

Then `nginx -t` and `systemctl reload nginx`. The block on `127.0.0.1:80` takes only requests to 127.0.0.1, the sites
facing outside stay as they are. The page shows counters only, no data of visitors. If nginx listens on another port,
adjust the URL in the module. The agent only reaches targets within `module_targets` (by default loopback).

## Settings

- **Status URL:** by default `http://127.0.0.1/nginx_status`.
- **Timeout**, for HTTPS **verify TLS** and a **CA file**.

## Metrics

- `sermon_nginx_up`: nginx answers.
- `sermon_nginx_connections_active`, `…_reading`, `…_writing`, `…_waiting`: open connections by state.
- `sermon_nginx_requests_per_second`, `sermon_nginx_connections_accepted_per_second`, `…_handled_per_second`.
- `sermon_nginx_connections_dropped_per_second`: connections turned away – raise `worker_connections`.

## When it doesn't work

- "status 404 – is stub_status enabled there?": the page isn't set up or the URL is wrong.
- "not a stub_status page": something else answers at the URL.
- `sermon_nginx_up` 0: nginx doesn't answer.
