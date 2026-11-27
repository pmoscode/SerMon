# Redis

Memory, clients, commands, hit ratio, evictions and replication of Redis or Valkey from `INFO`. The module is off by
default; the agent suggests it when it finds `redis-server` or `valkey-server` (process, unit, port 6379). It sends
only `AUTH` (with a password) and `INFO`, nothing that changes data.

## Requirements

- Redis within `module_targets` (by default loopback; names through `module_hosts`, ports through `module_ports`, by
  default 6379) or a Unix socket below `module_sockets` (by default `/run`,
  `/var/run`) the user `sermon` may use (group `redis` or `unixsocketperm`).
- With a password: best an ACL user of its own that may run `INFO` only:

  ```
  ACL SETUSER sermon on >long-password -@all +info
  ```

  The password is a credential: SerMon stores it encrypted, never shows it again, and the agent keeps it in memory
  only.

## Settings

- **Address:** by default `127.0.0.1:6379`; or a **Unix socket** instead of the address – the better way with a
  password.
- **User** (ACL, Redis 6 and later) and **password**; **timeout**.
- **TLS** (for the `tls-port` of Redis 6 and later): `off`, `verify` (the certificate against the system's CAs or the
  **CA file**, a PEM file below `module_file_prefixes`) or `no_verify` (any certificate: encrypted, but to whoever
  answers). Empty means: off on loopback and the socket, `verify` for any other address – as for PostgreSQL and MySQL.
  There is no TLS over the socket.
- **Password and strangers on the port:** `AUTH` sends the password in clear text; without TLS with `verify` it goes
  to whoever answers on the port. While Redis is stopped, another local user can take port `6379` and then gets the
  password. So with a password use the Unix socket (only Redis creates its path) or TLS with `verify`, and an ACL user
  that may run `INFO` only (above).

## Metrics

- `sermon_redis_memory_used_bytes`, `…_rss_bytes`, `…_max_bytes`; `sermon_redis_clients_connected`, `…_blocked`.
- `sermon_redis_commands_per_second`, `sermon_redis_keyspace_hit_ratio` and the hits and misses per second.
- `sermon_redis_evicted_keys_per_second`, `sermon_redis_expired_keys_per_second`,
  `sermon_redis_rejected_connections_per_second`, `sermon_redis_keys`.
- Replication: `sermon_redis_connected_replicas` (primary), `sermon_redis_master_link_up` (replica);
  `sermon_redis_rdb_last_save_ok`.

## When it doesn't work

- "Redis wants a password (NOAUTH)": set the password in the module.
- "AUTH failed: wrong user or password": user or password wrong (or Redis has no password at all).
- "no RESP reply (is it Redis?)": no Redis answers at the address.
- "TLS: …": Redis speaks no TLS at the address (set `tls` to `off` or use the `tls-port`), or its certificate doesn't
  match the name or the CA.
- `sermon_redis_up` 0: Redis doesn't answer.
