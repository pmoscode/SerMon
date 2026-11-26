# PostgreSQL

Connections, transactions, cache hits, database sizes, locks and replication of a PostgreSQL server (13 to 17). The
module is off by default, asks every 30 seconds in a short session (3 s per query, read-only) and needs a user of its
own with few rights.

## Create a monitoring user

```
CREATE ROLE sermon LOGIN PASSWORD '…' IN ROLE pg_monitor;
```

`pg_monitor` may read statistics, but no data. Without the role the module reports the error with the `GRANT` it
needs. The password is entered in the form only (it goes encrypted to the agents and is never shown again). Over the
Unix socket (`/run/postgresql/.s.PGSQL.5432`) `peer` authentication works without a password if the user is named like
the agent (`sermon`).

## Connection

- **Address** `host:port` (`127.0.0.1:5432` by default) or **Unix socket**. Hosts other than loopback only once
  `module_targets` in `agent.toml` allows them.
- **TLS:** `off` (loopback, socket), `verify` (the certificate against the system's CAs) or `no_verify`.
- Login with SCRAM-SHA-256, md5 or password; the module reports a wrong password as an error with the server's reason.

## Metrics

- `sermon_postgres_connections` by state and `sermon_postgres_connections_limit` (`max_connections`).
- `sermon_postgres_transactions_per_second`, `sermon_postgres_cache_hit_ratio`, `sermon_postgres_database_size_bytes`
  per database (the 16 largest).
- `sermon_postgres_locks`, `sermon_postgres_locks_waiting`.
- Replication: `sermon_postgres_replication_lag_seconds` per standby (on the primary), `sermon_postgres_standby` and
  `sermon_postgres_replay_lag_seconds` (on a standby).
