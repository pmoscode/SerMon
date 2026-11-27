# Backup und Restore

SerMon sichert sich mit `sermon-server backup` selbst: Stammdaten, Events, die Agent-CA und alles, was in der
Datenbank eingestellt wird, verschlüsselt in eine Datei. `sermon-server restore` spielt sie in eine **neue, leere**
Datenbank. Entscheidungen: [ADR 0042](../adr/0042-logisches-backup-und-restore.md). Grundlage ist die Compose aus der
[Installation](installation.md).

## Was gesichert wird – und was nicht

| Teil | Im Backup | Sichern Sie zusätzlich |
|---|---|---|
| Organisationen, Konten (Passwort-Hashes, TOTP, Passkeys, Recovery-Codes), Rollen, Einladungen | ja | |
| Nodes, Gruppen, Agent-CA und Zertifikate, Enrollment-Tokens | ja | |
| Security-Events, Alerts mit Verlauf, Audit-Logs (mit Hash-Kette) | ja | |
| Regeln, Kanäle, Policies, Silences, Wartungsfenster, Module, Log-Einstellungen, Update-Policies, Dashboards, Kiosk-Tokens | ja | |
| Zugangsdaten von Kanälen und Modulen | ja, **versiegelt** wie in der Datenbank | |
| Metriken (Rohdaten, 1-Minuten- und 1-Stunden-Werte), Anomalie-Werte | nein | volle Sicherung mit pgBackRest (unten) |
| Logs der Nodes | nein (Loki) | Volume `sermon_loki-data` (unten) |
| Anmeldungen (Sessions) | nein – nach einem Restore melden sich alle neu an | |
| **Master-Keys** (`/var/lib/sermon/master_keys`) | **nein** | **getrennt** sichern – ohne sie ist das Backup wertlos |
| `.env`, `secrets/`, `Caddyfile` | nein | als Dateien sichern |

Die Master-Keys stecken absichtlich nicht im Backup: Wer eine Backup-Datei und den Backup-Schlüssel erbeutet, hat ohne
sie weder die Agent-CA noch TOTP-Seeds oder Zugangsdaten. Umgekehrt lehnt der Restore ein Backup ab, das nicht zu den
Master-Keys passt.

## Einmal einrichten

1. **Backup-Schlüssel erzeugen** – am besten auf einem anderen Rechner; nötig ist nur das Image:

   ```sh
   docker compose run --rm --no-deps -T backup backup-keygen > sermon-backup-key.txt
   ```

   Die Datei enthält den **geheimen** Schlüssel (`AGE-SECRET-KEY-PQ-1…`, post-quanten-hybrid) und als Kommentar den
   öffentlichen. Bewahren Sie sie **nicht** auf dem Server auf (Passwortmanager, Tresor, zweiter Ort). Auf den Server
   kommt nur der öffentliche Schlüssel:

   ```sh
   sed -n 's/^# public key: //p' sermon-backup-key.txt > secrets/backup_recipients
   ```

   Mehrere Zeilen sind möglich (z. B. ein Schlüssel für das Team und einer für den Notfall-Umschlag). Schlüssel von
   `age-keygen` gehen auch; das Backup lässt sich dann ohne SerMon mit `age -d -i KEY | gunzip` öffnen.

2. **Master-Keys sichern** – einmal und nach jeder Rotation, getrennt von den Backups:

   ```sh
   docker compose cp server:/var/lib/sermon/master_keys ./master_keys.safe
   ```

3. **Installationen vor V1-05**: `./secrets.sh` legt `postgres_backup_password` und `database_backup_url` an; die
   Rolle `sermon_backup` gibt es in einem älteren Datenbank-Volume noch nicht. Einmal anlegen:

   ```sh
   ./secrets.sh
   docker compose up -d postgres
   docker compose exec -T postgres sh -c 'psql -v ON_ERROR_STOP=1 -U postgres -d sermon -v pw="$(cat /run/secrets/postgres_backup_password)"' <<'SQL'
   SELECT format('CREATE ROLE sermon_backup LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION BYPASSRLS IN ROLE pg_read_all_data PASSWORD %L', :'pw')
   WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'sermon_backup') \gexec
   SQL
   ```

## Sichern

```sh
mkdir -p backups
docker compose run --rm -T backup > backups/sermon-$(date -u +%Y%m%dT%H%MZ).backup
```

Das läuft im laufenden Betrieb: Das Backup liest einen Snapshot (eine Transaktion) als Rolle `sermon_backup`, die alles
lesen und nichts schreiben darf und die nur der einmalige Service `backup` kennt. Die Datei geht über stdout, der
Container braucht kein schreibbares Verzeichnis. Am Ende steht im Log (stderr) `backup complete` mit ID, Tabellen,
Zeilen und Bytes; mit `SERMON_LOG_LEVEL=debug` jede Tabelle. Bei einem Fehler ist der Exit-Code nicht 0 – verwerfen Sie
die Datei dann. Wer direkt in eine Datei schreibt (`sermon-server backup --out DATEI`), bekommt sie erst unter ihrem
Namen, wenn sie vollständig ist; eine vorhandene Datei wird nie überschrieben.

Für den Zeitplan genügt ein Cron-Job auf dem Host, z. B. täglich um 3 Uhr, und eine Aufbewahrung nach Ihren Regeln
(die Dateien sind verschlüsselt; Sie können sie zu einem Speicher außerhalb des Servers kopieren). Die Größe hängt an
Events und Audit-Logs, nicht an den Metriken.

## Wiederherstellen

Der Restore braucht eine Datenbank **ohne** Organisationen und User – nach einem Verlust also ein neues Volume oder einen
neuen Server. Auf einem neuen Server zuerst die [Installation](installation.md) bis einschließlich `./secrets.sh` und
`docker compose build`, aber **ohne** `init` und ohne `docker compose up`.

```sh
docker compose stop server                      # falls er läuft
docker compose up -d postgres                   # neues, leeres Volume
cp ./master_keys.safe secrets/master_keys       # die gesicherten Master-Keys
cp sermon-backup-key.txt secrets/backup_identity
chmod 0644 secrets/master_keys secrets/backup_identity   # der Container liest sie als eigener User
docker compose run --rm -T restore < backups/sermon-….backup
rm secrets/master_keys secrets/backup_identity
docker compose up -d
```

`restore` prüft zuerst den Backup-Schlüssel und die Master-Keys, migriert die leere Datenbank bis zum Stand des
Backups, lädt alles in **einer** Transaktion und prüft vor dem Commit das Siegel der Datei; danach migriert es auf den
Stand dieser Version (ein Restore mit einer neueren SerMon-Version geht). Die Master-Keys legt es in das Volume des
Servers (`/var/lib/sermon/master_keys`); liegen dort andere, lehnt es ab.

Danach:

- Alle melden sich neu an (Passwort und zweiter Faktor oder Passkey wie vorher).
- Die Agents verbinden sich von selbst wieder, mit ihren Zertifikaten; neu enrollen ist nicht nötig. Der Agent-Port
  muss unter demselben Namen erreichbar sein (`SERMON_DOMAIN`).
- Diagramme beginnen leer (keine Metriken im Backup), die Anomalie-Erkennung lernt neu, die Logs der letzten 14 Tage
  fehlen, wenn das Loki-Volume verloren ist.
- Was seit dem Backup gelöscht wurde (Konten, Nodes, Organisationen), ist wieder da: Löschungen aus dieser Zeit
  wiederholen (Datenschutz, `docs/privacy/`).

### Exit-Codes

| Code | Bedeutung |
|---|---|
| 0 | gesichert bzw. wiederhergestellt |
| 1 | Fehler (Verbindung, Konfiguration, Platte) |
| 2 | falscher Aufruf |
| 3 | **abgelehnt, nichts geändert:** kein passender Backup-Schlüssel, Master-Keys gehören nicht zum Backup, Datei beschädigt oder verändert, Datenbank nicht leer, Backup einer neueren Version, andere Master-Key-Datei im Volume |

Ein abgelehnter oder abgebrochener Restore lässt die Datenbank leer zurück (höchstens migriert); er lässt sich mit der
richtigen Datei oder dem richtigen Schlüssel wiederholen.

## Volle Sicherung mit Metriken: pgBackRest

Wer Metriken und Anomalie-Werte mitsichern will, sichert die Datenbank physisch, z. B. mit
[pgBackRest](https://pgbackrest.org/) (Voll-, differenzielle und inkrementelle Backups, WAL-Archiv für
Point-in-Time-Recovery). SerMon liefert dafür nichts mit; Hinweise:

- pgBackRest braucht Zugriff auf das Datenverzeichnis und `archive_command` der Datenbank: als eigener Container mit
  dem Volume `sermon_postgres-data` oder auf dem Host. Ein Restore geht nur in dieselbe Major-Version von PostgreSQL
  und dieselbe Version von TimescaleDB.
- Ein physisches Backup enthält auch die Rollen; die Master-Keys sichern Sie trotzdem getrennt.
- Das logische Backup von SerMon ersetzt es nicht und umgekehrt: Es bleibt der Weg in eine neue Version und der
  geprüfte, verschlüsselte Weg für die Stammdaten.

## Logs: das Loki-Volume

Loki hält die Logs der Nodes 14 Tage im Volume `sermon_loki-data`. Gesichert wird es als Volume, bei gestopptem Loki:

```sh
docker compose stop loki
docker run --rm -v sermon_loki-data:/loki:ro -v "$PWD/backups":/out busybox tar -C /loki -czf /out/loki-$(date -u +%F).tar.gz .
docker compose start loki
```

Zurück in ein leeres Volume entsprechend mit `tar -xzf`. Die Dateien sind **nicht** verschlüsselt und enthalten
personenbezogene Daten (Logins, IP-Adressen): verschlüsselt ablegen. Oft reicht es, nach einem Verlust ohne die Logs
neu zu beginnen – sie sind nach 14 Tagen ohnehin gelöscht.

## Getestet

- Integrationstest (`server/cmd/sermon-server/backup_integration_test.go`): Instanz mit Superadmin (Passwort, TOTP,
  Passkey), Agent, Zugangsdaten eines Moduls, Kiosk-Token, Dashboard, Events und Regel → Backup → neue Datenbank →
  Anmeldung mit Passwort und TOTP und mit dem Passkey, der Agent verbindet sich mit seinem Zertifikat und bekommt die
  Zugangsdaten, das Kiosk-Token öffnet sein Dashboard; kein Geheimnis im Klartext in der Datei; falscher
  Backup-Schlüssel, fremde Master-Keys, eine veränderte Datei und eine Datenbank mit Daten werden abgelehnt.
