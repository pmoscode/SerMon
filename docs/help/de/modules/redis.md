# Redis

Speicher, Clients, Befehle, Trefferquote, Verdrängungen und Replikation von Redis oder Valkey aus `INFO`. Das Modul
ist ab Werk aus; der Agent schlägt es vor, wenn er `redis-server` oder `valkey-server` findet (Prozess, Unit, Port
6379). Es sendet nur `AUTH` (mit Passwort) und `INFO`, nichts, was Daten ändert.

## Voraussetzungen

- Redis unter `module_targets` (ab Werk Loopback; Namen über `module_hosts`, Ports über `module_ports`, ab Werk 6379) oder ein Unix-Socket unter `module_sockets` (ab Werk `/run`,
  `/var/run`), auf den der User `sermon` zugreifen darf (Gruppe `redis` oder `unixsocketperm`).
- Mit Passwort: am besten ein eigener ACL-User, der nur `INFO` darf:

  ```
  ACL SETUSER sermon on >langes-passwort -@all +info
  ```

  Das Passwort ist eine Zugangsangabe: SerMon speichert es verschlüsselt, zeigt es nie wieder an, und der Agent hält
  es nur im Speicher.

## Einstellungen

- **Adresse:** ab Werk `127.0.0.1:6379`; oder **Unix-Socket** statt der Adresse – mit Passwort der bessere Weg.
- **User** (ACL, ab Redis 6) und **Passwort**; **Timeout**.
- **TLS** (für den `tls-port` ab Redis 6): `off`, `verify` (das Zertifikat gegen die CAs des Systems oder die
  **CA-Datei**, eine PEM-Datei unter `module_file_prefixes`) oder `no_verify` (jedes Zertifikat: verschlüsselt, aber an
  jeden, der antwortet). Leer heißt: aus auf Loopback und am Socket, `verify` für jede andere Adresse – wie bei
  PostgreSQL und MySQL. Über den Socket gibt es kein TLS.
- **Passwort und fremde Ports:** `AUTH` schickt das Passwort im Klartext; ohne TLS mit `verify` geht es an den, der
  auf dem Port antwortet. Ist Redis gestoppt, kann ein anderer lokaler Benutzer den Port `6379` belegen und bekommt
  dann das Passwort. Deshalb mit Passwort den Unix-Socket nehmen (seinen Pfad legt nur Redis an) oder TLS mit
  `verify`, und einen ACL-User, der nur `INFO` darf (oben).

## Metriken

- `sermon_redis_memory_used_bytes`, `…_rss_bytes`, `…_max_bytes`; `sermon_redis_clients_connected`, `…_blocked`.
- `sermon_redis_commands_per_second`, `sermon_redis_keyspace_hit_ratio` und die Treffer und Fehlgriffe je Sekunde.
- `sermon_redis_evicted_keys_per_second`, `sermon_redis_expired_keys_per_second`,
  `sermon_redis_rejected_connections_per_second`, `sermon_redis_keys`.
- Replikation: `sermon_redis_connected_replicas` (Primary), `sermon_redis_master_link_up` (Replika);
  `sermon_redis_rdb_last_save_ok`.

## Wenn es nicht läuft

- „Redis wants a password (NOAUTH)“: Passwort im Modul setzen.
- „AUTH failed: wrong user or password“: User oder Passwort falsch (oder Redis hat gar kein Passwort).
- „no RESP reply (is it Redis?)“: Unter der Adresse antwortet kein Redis.
- „TLS: …“: Redis spricht unter der Adresse kein TLS (dann `tls` auf `off` oder den `tls-port` nehmen), oder sein
  Zertifikat passt nicht zum Namen oder zur CA.
- `sermon_redis_up` 0: Redis antwortet nicht.
