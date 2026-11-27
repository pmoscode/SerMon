# 0042 – Logisches Backup und Restore

- Status: accepted
- Datum: 2026-11-27
- Bezug: KONZEPT.md §22 („Backup“), §18.5, §2 (D32); ADR 0007, ADR 0028, ADR 0031; IMPLEMENTIERUNGSPLAN V1-05, V1-07

## Kontext

KONZEPT §22 verlangt `sermon-server backup`: Stammdaten, Events, CA und Konfiguration verschlüsselt „per `pg_dump`“,
ohne Metrik-Rohdaten, und einen Restore in eine leere Datenbank, der in CI getestet wird. Offen war, wie das zur
Mandantentrennung (ADR 0007) und zur gehärteten Compose (ADR 0028) passt:

- `FORCE ROW LEVEL SECURITY` gilt auch für den Eigentümer `sermon_owner`; ohne Mandant sieht er keine Zeile. Ein
  Backup als `sermon_owner` oder `sermon_app` wäre still leer. `COPY FROM` geht in Tabellen mit RLS gar nicht.
- Das Server-Image ist distroless ohne `pg_dump`; dessen Version müsste zum Server passen, ein Dump von TimescaleDB
  braucht `timescaledb_pre_restore()` als Eigentümer der Datenbank und dieselbe Extension-Version.
- Der Audit-Log berechnet seine Hash-Kette in einem Trigger (`clock_timestamp()`): ein naives Einspielen schreibt
  eine neue Kette.
- Backups sind langlebig und liegen außerhalb des Servers – ein hochwertiges Asset (Konten, Mandantendaten).

## Optionen

1. **`pg_dump`/`pg_restore`** im Backup-Container – bekanntes Format, aber ein zweites Image mit passender
   Client-Version, Sonderbehandlung von TimescaleDB, Restore als Eigentümer der Datenbank mit Superuser-nahen Schritten;
   die Verschlüsselung käme außen herum dazu.
2. **Eigener logischer Dump über `COPY` im Server-Binary** – ein Snapshot (`REPEATABLE READ READ ONLY`), je Tabelle
   `COPY (SELECT …) TO STDOUT`, das Schema kommt beim Restore aus den Migrationen derselben Version. Kein zweites
   Image, keine Abhängigkeit von Client-Versionen, Tabellen und Grenzen in unserer Hand.
3. **Nur pgBackRest** (physisch) – vollständig mit Metriken, aber ohne Verschlüsselung durch uns, ohne Auswahl und nur
   in dieselbe Major-Version.

Für die Verschlüsselung:

- a. **age** (`filippo.io/age`, BSD-3, Filippo Valsorda; Format age-encryption.org/v1) mit öffentlichen Schlüsseln –
  geprüfte Streaming-Konstruktion (ChaCha20-Poly1305 je 64 KiB, authentifiziert, Abschneiden erkannt), mit `age`
  auch ohne SerMon lesbar, post-quanten-hybride Schlüssel (ML-KEM-768 + X25519).
- b. **Schlüssel aus den Master-Keys** (HKDF, AES-GCM in Blöcken) – ohne neue Abhängigkeit, aber eine eigene
  Stream-Konstruktion und die Master-Keys im Backup-Container müssten auch zum Entschlüsseln reichen.
- c. **Passphrase** (scrypt) – ein Geheimnis, das der Backup-Job kennen müsste.

## Entscheidung

Gewählt: **Option 2 mit age (a)**, dazu eine Bindung an die Master-Keys. pgBackRest bleibt der Weg für die volle
Sicherung mit Metriken (Option 3, Anleitung in `docs/admin/backup.md`). KONZEPT §22 sagt jetzt „logisch per `COPY`“
statt „per `pg_dump`“.

- **Inhalt:** jede Tabelle des Schemas `public` außer einer festen Liste (`postgres.BackupExclusions`):
  `metrics_raw` (Rohdaten; die Continuous Aggregates `metrics_1m`/`metrics_1h` hängen daran und lassen sich logisch
  nicht ohne Superuser-Rechte und Interna von TimescaleDB einspielen – sie gehören zu pgBackRest), `anomaly_points`
  (aus den Metriken abgeleitet), `sessions` (alle melden sich neu an), `oidc_flows` und `webauthn_ceremonies` (laufende
  Anmeldungen), die Leases der Instanzen (`alert_leases`, `update_leases`, `anomaly_leases`) und `goose_db_version`.
  Eine neue Tabelle ist also automatisch drin; eine Tabelle, die eine ausgelassene referenziert, bricht das Backup ab.
  Partitionen (`security_events`) über ihre Wurzel, Sequenzen mit Stand. Mit drin: `series` (die Baselines der
  Anomalien hängen daran), `anomaly_baselines`, die Agent-CA, alle versiegelten Zugangsdaten (bleiben versiegelt),
  Audit-Logs mit Hash-Kette.
- **„Konfiguration“** heißt alles, was in der Datenbank eingestellt wird (Regeln, Kanäle, Policies, Module,
  Log-Einstellungen, Update-Policies, Dashboards, Kiosk-Tokens). Was als Datei auf dem Host liegt (`.env`,
  `secrets/`, `Caddyfile`, Master-Keys), sichert der Betreiber als Datei; die Anleitung nennt es.
- **Rollen:** Das Backup liest als neue Rolle **`sermon_backup`** (`LOGIN BYPASSRLS`, Mitglied von
  `pg_read_all_data`, sonst nichts; angelegt von `initdb/010-roles.sh`, für ältere Volumes ein dokumentierter
  Einmal-Schritt). Nur so sieht ein Snapshot alle Organisationen; die Rolle kann nichts schreiben. `row_security = off`
  im Snapshot: Eine Rolle, für die RLS doch gilt, bekommt einen Fehler statt eines unvollständigen Backups (Test). Den
  Restore macht `sermon_owner` (er migriert ohnehin): in **einer Transaktion** hebt er `FORCE ROW LEVEL SECURITY` der
  Tabellen und ihrer Partitionen auf, prüft, dass es weder User noch Organisationen gibt, leert die Tabellen, schaltet
  ihre aktiven Trigger ab (die Hash-Kette bleibt), lädt per `COPY FROM`, setzt die Sequenzen und stellt Trigger und
  `FORCE` vor dem Commit wieder her; zuletzt schreibt er `instance.restored` (Backup-ID, Zeitpunkt, Version) ins
  instanzweite Audit-Log, an das Ende der wiederhergestellten Kette. Die Sperren der Tabellen halten den Server währenddessen fern. Das ist die zweite
  bewusste Ausnahme nach Migration 00012 (ADR 0007); ein Test prüft danach `FORCE` auf jeder Tabelle.
- **Schema:** Der Kopf nennt die angewandten Migrationen. Der Restore migriert eine leere Datenbank genau bis dorthin,
  lädt und migriert dann auf den Stand des Binaries (Restore mit einer neueren Version geht). Kennt das Binary eine
  Migration des Backups nicht oder hat die Datenbank eine, die das Backup nicht hat, wird abgelehnt.
- **Format** (`platform/backupfile`): age-Datei an die öffentlichen Backup-Schlüssel (`SERMON_BACKUP_RECIPIENTS`), darin
  gzip, darin Datensätze (Art, Länge, Inhalt; Kopf, Tabelle, `COPY`-Text in Blöcken zu 256 KiB, Ende mit Zeilenzahl,
  Sequenzen, Siegel). Höchstens 1 MiB je Datensatz, Namen von Tabellen, Spalten und Sequenzen nur `[a-z_][a-z0-9_]*`
  und beim Restore gegen das Schema geprüft; der Leser ist gefuzzt.
- **Bindung an die Master-Keys:** Der Kopf trägt seine ID versiegelt mit `secrets.Box`, das Siegel am Ende ist der
  SHA-256 aller Datensätze davor, ebenfalls versiegelt. Mit fremden Master-Keys bricht der Restore vor der Datenbank ab;
  eine Datei, die jemand mit dem öffentlichen Schlüssel selbst gebaut oder verändert hat, scheitert am Siegel, und die
  Transaktion wird zurückgerollt. Die **Master-Keys sind nicht im Backup**; ohne sie sind CA, TOTP-Seeds und
  Zugangsdaten ohnehin unbrauchbar. Sie werden getrennt gesichert (einmal und nach jeder Rotation); `restore
  --install-master-keys` legt die gesicherte Datei in das Volume des Servers und lehnt eine abweichende vorhandene ab.
- **Schlüssel:** `sermon-server backup-keygen` erzeugt einen hybriden age-Schlüssel (`AGE-SECRET-KEY-PQ-1…`); der
  Server kennt nur den öffentlichen. Der geheime liegt beim Betreiber und nur für einen Restore im Container
  (`SERMON_BACKUP_IDENTITY`). `age-keygen`-Schlüssel (X25519) gehen auch; andere Arten werden abgelehnt.
- **Betrieb:** einmalige Services `backup` und `restore` (Profile, `docker compose up` startet sie nie), read-only,
  ohne Capabilities, Secrets als Dateien; die Datei geht über stdout/stdin (`docker compose run --rm -T …`), der
  Container braucht kein schreibbares Volume. `--out DATEI` schreibt erst `DATEI.partial` (0600) und benennt erst
  nach `fsync` um, nie über eine vorhandene Datei. Fortschritt je Tabelle im Debug-Log, am Ende ID, Tabellen, Zeilen,
  Bytes. Exit-Codes: 0 gut, 1 Fehler, 2 Aufruf, **3 abgelehnt** (Schlüssel, beschädigt/verändert, Datenbank nicht leer,
  anderes Schema, andere Master-Key-Datei) – nichts wurde geändert. `check-compose --hardened` prüft auch die Services
  der Profile und verlangt die URL von `sermon_backup` und den geheimen Backup-Schlüssel nur in einmaligen Containern.

## Konsequenzen

- Positiv: ein Binary, kein zweites Image, keine Versionsfalle von `pg_dump`; Backups sind ohne den geheimen Schlüssel
  nicht lesbar, auch nicht für den Server, der sie schreibt; der Restore ist atomar und geprüft; mit `age` lassen sich
  Backups auch ohne SerMon öffnen (`age -d -i key | gunzip`).
- Negativ / Risiken: Nach einem Restore fehlen Metriken und Anomalie-Werte (Diagramme beginnen leer, Anomalien lernen
  neu); dafür pgBackRest. Der Restore braucht eine leere Datenbank und hält während des Ladens alle Tabellen gesperrt.
  Die Größe ist nicht begrenzt (Stammdaten und Events: wenige hundert MiB bei 50 Nodes); ein Backup ist ein Snapshot,
  lange Transaktionen halten das Vacuum auf. Neue Abhängigkeit `filippo.io/age` (mit `filippo.io/hpke`), geprüft:
  Lizenz BSD-3, gepflegt, `govulncheck` sauber.
- Security-Auswirkungen: neues Asset „Backup-Dateien“ und „geheimer Backup-Schlüssel“ (Threat Model A13, A14, TB9,
  T116 bis T119); `sermon_backup` liest alle Organisationen und existiert nur im Backup-Container; Kompression vor
  Verschlüsselung ist unbedenklich, weil niemand die Größe einzelner Backups mit eigenen Eingaben gezielt beobachten
  und vergleichen kann und die Secrets darin ohnehin versiegelt oder gehasht sind.
- Folgearbeiten: V1-09 (Admin-Doku: Zeitplan, Aufbewahrung), Restore in CI (V1-07).
