# PostgreSQL

Verbindungen, Transaktionen, Cache-Treffer, Datenbankgrößen, Sperren und Replikation eines PostgreSQL-Servers (13 bis
17). Das Modul ist ab Werk aus, fragt alle 30 Sekunden mit einer kurzen Sitzung (Zeitgrenze 3 s je Abfrage, nur
lesend) und braucht dafür einen eigenen User mit wenigen Rechten.

## Monitoring-User anlegen

```
CREATE ROLE sermon LOGIN PASSWORD '…' IN ROLE pg_monitor;
```

`pg_monitor` darf Statistiken lesen, aber keine Daten. Ohne die Rolle meldet das Modul den Fehler mit dem nötigen
`GRANT`. Das Passwort steht nur im Formular (es geht verschlüsselt an die Agents und wird nie wieder angezeigt).
Über den Unix-Socket (`/run/postgresql/.s.PGSQL.5432`) geht es mit `peer` auch ohne Passwort, wenn der User wie der
Agent heißt (`sermon`).

## Verbindung

- **Adresse** `host:port` (ab Werk `127.0.0.1:5432`) oder **Unix-Socket**. Andere Hosts als Loopback erst, wenn
  `module_targets` in `agent.toml` sie erlaubt.
- **TLS:** `off` (Loopback, Socket), `verify` (Zertifikat gegen die CAs des Systems) oder `no_verify`.
- Anmeldung mit SCRAM-SHA-256, md5 oder Passwort; ein falsches Passwort meldet das Modul als Fehler mit dem Grund des
  Servers.

## Metriken

- `sermon_postgres_connections` je Zustand und `sermon_postgres_connections_limit` (`max_connections`).
- `sermon_postgres_transactions_per_second`, `sermon_postgres_cache_hit_ratio`, `sermon_postgres_database_size_bytes`
  je Datenbank (die 16 größten).
- `sermon_postgres_locks`, `sermon_postgres_locks_waiting`.
- Replikation: `sermon_postgres_replication_lag_seconds` je Standby (auf dem Primary), `sermon_postgres_standby` und
  `sermon_postgres_replay_lag_seconds` (auf einem Standby).
