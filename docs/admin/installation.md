# Installation mit docker compose

Diese Anleitung richtet SerMon auf einem Linux-Server mit Docker ein. Grundlage sind die Dateien in
[`deploy/compose/`](../../deploy/compose/) (Entscheidungen: [ADR 0028](../adr/0028-produktions-compose.md)).

## Voraussetzungen

- Linux-Server mit Docker Engine und dem Compose-Plugin (`docker compose version`).
- Richtwert für 50 Nodes: 4 vCPU, 8 GB RAM, 200 GB SSD (KONZEPT §22).
- Ein DNS-Name, der auf den Server zeigt, z. B. `sermon.example.org`.
- Offene Ports: **80** und **443** (Web-UI, Zertifikat von Let's Encrypt) sowie **8443** (Agents, mTLS).
- Ein Mailserver mit STARTTLS (Port 587) oder TLS (Port 465). Ohne Mail geht es in Produktion nicht: Einladungen gehen
  nur per Mail raus, damit die Adresse bewiesen ist.
- `openssl` für `secrets.sh`, `git` zum Holen des Quellcodes.

## Was läuft

| Dienst | Aufgabe | Erreichbar |
|---|---|---|
| `caddy` | TLS mit Zertifikat per ACME, Reverse-Proxy für die Web-UI und das Enrollment | 80, 443 |
| `server` | `sermon-server`, gebaut aus dem Quellcode; Web-API, Ingest, Alerting | 8443 (Agents, mTLS) |
| `migrate` | dasselbe Image, läuft vor dem Server einmal durch und migriert die Datenbank | – |
| `postgres` | PostgreSQL 17 mit TimescaleDB | nur im internen Netz |
| `loki` | Logs der Nodes, getrennt pro Organisation, 14 Tage ([ADR 0031](../adr/0031-logs-in-loki.md)) | nur im internen Netz |

Alle Container laufen mit schreibgeschütztem Dateisystem, ohne Capabilities (Ausnahmen: Caddy darf Port 80/443
binden, PostgreSQL zum eigenen Benutzer wechseln) und ohne neue Privilegien. Die Datenbank hängt nur an einem Netz
ohne Route nach außen, ebenso Loki: Loki hat keine eigene Anmeldung, nur der Server erreicht es und nennt bei jedem
Zugriff die Organisation. Der Agent-Port geht direkt zum Server, damit er die echten Adressen der Agents sieht.

Die Datenbank hat drei Rollen ([ADR 0007](../adr/0007-mandantentrennung-rls.md)): Der Superuser `postgres` bleibt im
Datenbank-Container; `sermon_owner` besitzt das Schema und migriert, aber nur im Service `migrate`; der Server meldet
sich als `sermon_app` an, für die die Mandantentrennung per Row-Level-Security immer gilt. Ein übernommener Server
kommt so weder an die Daten fremder Organisationen ohne RLS noch an das Schema.

## Einrichten

```sh
git clone https://github.com/pmoscode/SerMon.git
cd SerMon/deploy/compose

cp env.example .env
$EDITOR .env          # SERMON_DOMAIN, SERMON_ACME_EMAIL, SMTP

./secrets.sh          # Datenbank-Passwörter und -URLs in ./secrets/
$EDITOR secrets/smtp_password   # nur mit SERMON_SMTP_USERNAME: das Passwort, ohne Zeilenumbruch

docker compose build
docker compose run --rm migrate init --master-keys-file /var/lib/sermon/master_keys
docker compose up -d
```

`init` läuft im Service `migrate`, legt die Master-Key-Datei an, migriert die Datenbank, erzeugt die Agent-CA und gibt ein **Setup-Token** aus.
Es ist 30 Minuten gültig und funktioniert genau einmal. Öffnen Sie `https://<SERMON_DOMAIN>`, geben Sie das Token ein und
legen Sie die erste Organisation und den ersten Superadmin an. Ist das Token abgelaufen, schreibt der Server bei jedem
Start ein neues ins Log:

```sh
docker compose logs server | grep setup_token
```

Nach der Einrichtung verweigert `init` jede weitere Ausführung.

## Agents anbinden

In der UI unter **Nodes → Node hinzufügen** entsteht ein Befehl mit Enrollment-Token. Der Agent meldet sich danach
über `<SERMON_DOMAIN>:8443`. Für die Installation per `install.sh` braucht der Server signierte Agent-Binaries:

- `SERMON_RELEASE_KEY` in `.env` ist der öffentliche Release-Schlüssel (`scripts/release-sign.sh pubkey KEY.pem`); er
  wird beim Bauen einkompiliert, also nach einer Änderung `docker compose build`.
- Die signierten Binaries (`sermon-agent-linux-amd64`, `sermon-agent-linux-arm64` mit ihren Signaturen) liegen in
  `deploy/compose/downloads/`.

Ohne Release-Schlüssel verweigert `install.sh` die Installation; die Agent-Pakete (.deb, .rpm, .apk) gehen auch ohne.
Veröffentlichte Releases mit Schlüssel und Binaries folgen mit v1.0.

## Was wo liegt

| Was | Wo | Hinweis |
|---|---|---|
| Einstellungen | `deploy/compose/.env` | keine Geheimnisse |
| Datenbank-Passwörter, SMTP-Passwort | `deploy/compose/secrets/` | `secrets.sh` überschreibt nie; ein neues Passwort sperrt den Server aus. `postgres_password` (Superuser) braucht nur der Datenbank-Container |
| Master-Keys, Agent-CA | Volume `sermon_server-state` | **sichern**: ohne Master-Keys sind alle versiegelten Secrets verloren |
| Datenbank | Volume `sermon_postgres-data` | |
| Logs | Volume `sermon_loki-data` | 14 Tage; Konfiguration in `deploy/compose/loki.yaml` |
| Zertifikate von Caddy | Volume `sermon_caddy-data` | |

Die Logs der Container sind auf 5 × 10 MB begrenzt.

## Aktualisieren

```sh
git pull
docker compose build
docker compose up -d
```

Die Migrationen spielt der Service `migrate` ein, bevor der Server startet; schlägt das fehl, startet der Server nicht
(`docker compose logs migrate`). Vor einem Update die Volumes sichern (bei gestoppten Containern); ein
eigener Befehl `sermon-server backup` mit getestetem Restore folgt mit v1.0.

## Self-Monitoring

Mit `SERMON_DATABASE_SIZE_LIMIT` (z. B. `150GiB`) warnt der Server die Superadmins per Mail und in der UI, sobald die
Datenbank 80 % davon erreicht ([ADR 0027](../adr/0027-server-ereignisse-und-self-monitoring.md)). Der Server kennt nur
die Größe der Datenbank, nicht den freien Platz: Setzen Sie die Grenze unter den Platz des Datenträgers. Den Füllstand
der Volumes misst ein Agent auf dem Server-Host.

## Länder im Security-Dashboard (optional)

Der Server kann zu den Adressen im Security-Dashboard das Land zeigen. Er lädt dafür nichts herunter: Legen Sie eine
Datenbank im MaxMind-DB-Format nach `deploy/compose/geoip/`, etwa „IP to Country Lite“ von DB-IP
(<https://db-ip.com/db/lite.php>, CC BY 4.0) oder GeoLite2 Country von MaxMind (Konto nötig), und setzen Sie in `.env`
`SERMON_GEOIP_DB_FILE=/srv/geoip/<datei>.mmdb`. Den Lizenzhinweis zeigt das Dashboard neben den Ländern; er ergibt
sich aus der Datei, `SERMON_GEOIP_ATTRIBUTION` ersetzt ihn. Private Adressen haben kein Land. Die Datei liest der Server
beim Start; ist sie älter als 90 Tage, warnt er im Log. Zum Aktualisieren die Datei ersetzen und den Server neu
starten (`docker compose restart server`).

## Organisationen löschen

Eine zum Löschen vorgemerkte Organisation bleibt gesperrt, bis die Frist abläuft; danach löscht der Server sie mit
allen Daten (Prüfung stündlich, [ADR 0029](../adr/0029-organisationen-verwalten-sperren-loeschen.md)). Die Frist
setzt `SERMON_ORG_DELETION_GRACE` (Standard `720h` = 30 Tage, mindestens `168h` = 7 Tage). Wer das Audit-Log einer
Organisation aufbewahren muss, lädt es während der Sperre auf der Seite „Organisationen“ herunter.

## Wenn die Standardnetze kollidieren

Das Netz `edge` nutzt `172.30.78.0/24`, Caddy hat darin die feste Adresse `172.30.78.10`, dynamische Adressen kommen
aus `172.30.78.128/25`. Kollidiert das mit einem vorhandenen Netz, setzen Sie in `.env` `SERMON_EDGE_SUBNET`,
`SERMON_EDGE_IP_RANGE` und `SERMON_PROXY_IP` passend; die Adresse von Caddy muss außerhalb des dynamischen Bereichs
liegen. Nur sie darf dem Server die Adresse des Browsers nennen (`X-Forwarded-For`).

## Installationen vor M2-36

Bis M2-36 migrierte der Server selbst, als Superuser `sermon`. Solche Installationen (vor dem ersten Release, nur zum
Ausprobieren) bitte neu aufsetzen: `docker compose down -v`, `./secrets/` löschen und die Schritte unter
[Einrichten](#einrichten) wiederholen. Daten gehen dabei verloren.
