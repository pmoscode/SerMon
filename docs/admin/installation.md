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
| `postgres` | PostgreSQL 17 mit TimescaleDB | nur im internen Netz |

Alle Container laufen mit schreibgeschütztem Dateisystem, ohne Capabilities (Ausnahmen: Caddy darf Port 80/443
binden, PostgreSQL zum eigenen Benutzer wechseln) und ohne neue Privilegien. Die Datenbank hängt nur an einem Netz
ohne Route nach außen. Der Agent-Port geht direkt zum Server, damit er die echten Adressen der Agents sieht.

## Einrichten

```sh
git clone https://github.com/pmoscode/SerMon.git
cd SerMon/deploy/compose

cp env.example .env
$EDITOR .env          # SERMON_DOMAIN, SERMON_ACME_EMAIL, SMTP

./secrets.sh          # Datenbank-Passwörter und -URLs in ./secrets/
$EDITOR secrets/smtp_password   # nur mit SERMON_SMTP_USERNAME: das Passwort, ohne Zeilenumbruch

docker compose build
docker compose run --rm server init --master-keys-file /var/lib/sermon/master_keys
docker compose up -d
```

`init` legt die Master-Key-Datei an, migriert die Datenbank, erzeugt die Agent-CA und gibt ein **Setup-Token** aus.
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
| Datenbank-Passwörter, SMTP-Passwort | `deploy/compose/secrets/` | `secrets.sh` überschreibt nie; ein neues Passwort sperrt den Server aus |
| Master-Keys, Agent-CA | Volume `sermon_server-state` | **sichern**: ohne Master-Keys sind alle versiegelten Secrets verloren |
| Datenbank | Volume `sermon_postgres-data` | |
| Zertifikate von Caddy | Volume `sermon_caddy-data` | |

Die Logs der Container sind auf 5 × 10 MB begrenzt.

## Aktualisieren

```sh
git pull
docker compose build
docker compose up -d
```

Migrationen laufen beim Start automatisch. Vor einem Update die Volumes sichern (bei gestoppten Containern); ein
eigener Befehl `sermon-server backup` mit getestetem Restore folgt mit v1.0.

## Self-Monitoring

Mit `SERMON_DATABASE_SIZE_LIMIT` (z. B. `150GiB`) warnt der Server die Superadmins per Mail und in der UI, sobald die
Datenbank 80 % davon erreicht ([ADR 0027](../adr/0027-server-ereignisse-und-self-monitoring.md)). Der Server kennt nur
die Größe der Datenbank, nicht den freien Platz: Setzen Sie die Grenze unter den Platz des Datenträgers. Den Füllstand
der Volumes misst ein Agent auf dem Server-Host.

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
