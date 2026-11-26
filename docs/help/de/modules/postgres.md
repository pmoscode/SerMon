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

- **Unix-Socket** (`/run/postgresql/.s.PGSQL.5432`) ist der beste Weg: Den Pfad legt nur PostgreSQL an, und mit `peer`
  braucht es kein Passwort. Sonst **Adresse** `host:port` (ab Werk `127.0.0.1:5432`); andere Hosts als Loopback erst,
  wenn `module_targets` in `agent.toml` sie erlaubt.
- **TLS:** `off`, `verify` (Zertifikat gegen die CAs des Systems oder die **CA-Datei** unter `module_file_prefixes`)
  oder `no_verify` (verschlüsselt, aber an jeden, der antwortet). Leer heißt: `off` auf Loopback und am Socket,
  `verify` für jede andere Adresse.
- **Anmeldung** (`auth`): ab Werk nur **SCRAM-SHA-256** – dabei beweist auch der Server, dass er das Passwort kennt; ein
  Server, der das überspringt oder auf eine schwächere Methode wechselt, bekommt nichts. `md5` lässt zusätzlich md5 zu
  (PostgreSQL 13 und ältere Passwörter: besser `password_encryption = scram-sha-256` setzen und das Passwort neu
  vergeben). `password` lässt das Passwort selbst zu, aber nur über den Socket oder mit `verify`. Ein falsches
  Passwort meldet das Modul als Fehler mit dem Grund des Servers.
- **Warum so streng:** Ist PostgreSQL gestoppt oder hört es nur am Socket, kann ein anderer lokaler Benutzer den Port
  `5432` belegen und sich als Datenbank ausgeben. Er bekommt dann weder das Passwort noch einen md5-Hash – höchstens
  einen SCRAM-Beweis, aus dem sich nur mit Durchprobieren ein schwaches Passwort erraten lässt. Deshalb: Socket
  bevorzugen, ein langes zufälliges Passwort und einen User nur mit `pg_monitor`.

## Metriken

- `sermon_postgres_connections` je Zustand und `sermon_postgres_connections_limit` (`max_connections`).
- `sermon_postgres_transactions_per_second`, `sermon_postgres_cache_hit_ratio`, `sermon_postgres_database_size_bytes`
  je Datenbank (die 16 größten).
- `sermon_postgres_locks`, `sermon_postgres_locks_waiting`.
- Replikation: `sermon_postgres_replication_lag_seconds` je Standby (auf dem Primary), `sermon_postgres_standby` und
  `sermon_postgres_replay_lag_seconds` (auf einem Standby).
