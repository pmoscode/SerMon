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

- The **Unix socket** (`/run/postgresql/.s.PGSQL.5432`) is the best way: only PostgreSQL creates that path, and with
  `peer` no password is needed. Otherwise the **address** `host:port` (`127.0.0.1:5432` by default); hosts other than
  loopback only once `module_targets` in `agent.toml` allows them.
- **TLS:** `off`, `verify` (the certificate against the system's CAs or the **CA file** below `module_file_prefixes`)
  or `no_verify` (encrypted, but to whoever answers). Empty means `off` on loopback and the socket, `verify` for any
  other address.
- **Authentication** (`auth`): by default only **SCRAM-SHA-256** – the server proves that it knows the password too; a
  server that skips that or switches to a weaker method gets nothing. `md5` also allows md5 (PostgreSQL 13 and older
  passwords: better set `password_encryption = scram-sha-256` and set the password again). `password` allows the
  password itself, but only over the socket or with `verify`. The module reports a wrong password as an error with
  the server's reason.
- **Why so strict:** while PostgreSQL is stopped or listens on its socket only, another local user can take port
  `5432` and pose as the database. They get neither the password nor an md5 hash – at most a SCRAM proof, from which
  only guessing can recover a weak password. So: prefer the socket, use a long random password and a user with
  `pg_monitor` only.

## Metrics

- `sermon_postgres_up`: 1 if the server answered, 0 if nothing accepts the connection (default rule "PostgreSQL down"
  after 2 minutes).
- `sermon_postgres_connections` by state and `sermon_postgres_connections_limit` (`max_connections`).
- `sermon_postgres_transactions_per_second`, `sermon_postgres_cache_hit_ratio`, `sermon_postgres_database_size_bytes`
  per database (the 16 largest).
- `sermon_postgres_locks`, `sermon_postgres_locks_waiting`.
- Replication: `sermon_postgres_replication_lag_seconds` per standby (on the primary), `sermon_postgres_standby` and
  `sermon_postgres_replay_lag_seconds` (on a standby).
