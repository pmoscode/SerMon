# Konfiguration des Servers (Referenz)

`sermon-server` liest seine Einstellungen nur aus Umgebungsvariablen. Jede Variable `SERMON_X` geht auch als Datei:
`SERMON_X_FILE=/pfad` liest den Wert aus der Datei (ohne abschließenden Zeilenumbruch), etwa aus einem Docker-Secret.
Beides zugleich ist ein Fehler. Der Server prüft alles beim Start und nennt alle Fehler auf einmal; Fehlermeldungen
zeigen nie Passwörter oder Schlüssel.

In [`deploy/compose`](../../../deploy/compose/) setzt `compose.yaml` die meisten Werte selbst; was Sie ändern,
steht in `.env` (Vorlage `env.example`, siehe [Installation](../installation.md)). Die Spalte „compose“ nennt, was
dort gilt. Ein Test (`server/cmd/sermon-server/admin_docs_test.go`) verlangt, dass jede Variable des Servers hier
steht und die Voreinstellungen dem Code entsprechen.

Verwandt: [Härtung](hardening.md) (welche Werte die Sicherheit betreffen), [Betrieb](operations.md).

## Allgemein

| Variable | ab Werk | compose | Bedeutung |
|---|---|---|---|
| `SERMON_MODE` | `production` | – | `development` erlaubt den Betrieb ohne SMTP (Einladungslinks stehen dann in der UI und beweisen keine Adresse), `http://`-Origins und SMTP ohne TLS. **Nie in Produktion**; der Server warnt beim Start. |
| `SERMON_HTTP_ADDR` | `:8080` | `:8080` | Adresse der Web-UI und Web-API (`host:port`), hinter Caddy. |
| `SERMON_LOG_LEVEL` | `info` | – | `debug`, `info`, `warn` oder `error`. |
| `SERMON_LOG_FORMAT` | `json` | `json` | `json` oder `text`. |
| `SERMON_SHUTDOWN_TIMEOUT` | `15s` | – | So lange dürfen laufende Anfragen nach `SIGTERM` noch dauern. |

## Datenbank und Schlüssel

| Variable | ab Werk | compose | Bedeutung |
|---|---|---|---|
| `SERMON_DATABASE_URL` | – | `secrets/database_url` | Verbindung als `sermon_app` (ohne Sonderrechte, RLS gilt). Leer: keine Datenbank, nur UI und Health. Der Server lehnt Rollen mit Superuser- oder `BYPASSRLS`-Rechten ab. |
| `SERMON_DATABASE_MIGRATE_URL` | – | nur im Service `migrate` | Verbindung als Owner `sermon_owner` für Migrationen (`sermon-server migrate`, `init`) und den Restore. Gehört nie in den Server-Container. |
| `SERMON_MASTER_KEYS` | – | Datei `/var/lib/sermon/master_keys` im Volume `sermon_server-state` | Pflicht mit Datenbank. Eine Zeile `<id>:<base64>` je Schlüssel, der neueste zuerst; anlegen mit `sermon-server init --master-keys-file PFAD`. **Nie ersetzen, kürzen oder umsortieren** und getrennt sichern ([Backup](../backup.md)). |
| `SERMON_DATABASE_SIZE_LIMIT` | – | aus `.env` | Größe, ab der das Self-Monitoring warnt, z. B. `150GiB` (Bytes oder `KiB`, `MiB`, `GiB`, `TiB`). Leer: keine Prüfung. Siehe [Betrieb](operations.md#self-monitoring). |

## Web-UI, Proxy und ausgehende Verbindungen

| Variable | ab Werk | compose | Bedeutung |
|---|---|---|---|
| `SERMON_ALLOWED_ORIGINS` | – | `https://<SERMON_DOMAIN>` | Exakte Origins der UI, mit Komma getrennt (`https://host[:port]`). Schreibende Aufrufe mit anderem `Origin` lehnt der Server ab; leer heißt: **alle** schreibenden Aufrufe abgelehnt. In Produktion nur `https://`. Der erste Origin steht in Mails und ist die Rücksprungadresse von OIDC. Pflicht mit SMTP. |
| `SERMON_TRUSTED_PROXIES` | – | die feste Adresse von Caddy als `/32` | CIDRs, die `X-Forwarded-For` setzen dürfen. Nur der eigene Reverse-Proxy – sonst kann jeder seine Adresse für Rate-Limits und Audit-Log selbst wählen. |
| `SERMON_OUTBOUND_ALLOWLIST` | – | aus `.env` | Interne Netze, die Kanäle, Webhooks und der OIDC-Provider erreichen dürfen (`10.20.0.0/16` oder Adressen, mit Komma getrennt). Leer: nur öffentliche Adressen. Netze, die alles erlauben, und Bereiche mit eingebetteter IPv4 lehnt der Server ab; Metadaten-Dienste der Clouds bleiben immer gesperrt ([ADR 0024](../../adr/0024-ausgehende-verbindungen-safehttp.md)). |
| `SERMON_METRICS_ADDR` | – | nicht gesetzt | Eigener Listener für `/metrics` ohne Anmeldung (nur Werte der Instanz, keine Mandantendaten, [ADR 0011](../../adr/0011-server-metriken-endpunkt.md)). Leer: aus. Muss sich von `SERMON_HTTP_ADDR` und `SERMON_AGENT_ADDR` unterscheiden; außerhalb von Loopback warnt der Server. |
| `SERMON_GEOIP_DB_FILE` | – | aus `.env` (`/srv/geoip/…`) | Länderdatenbank im MaxMind-DB-Format für das Security-Dashboard; der Server lädt nichts herunter. |
| `SERMON_GEOIP_ATTRIBUTION` | – | aus `.env` | Ersetzt den Lizenzhinweis, der sich aus der Datei ergibt (höchstens 300 Bytes); nur mit `SERMON_GEOIP_DB_FILE`. |

## Agents

| Variable | ab Werk | compose | Bedeutung |
|---|---|---|---|
| `SERMON_AGENT_ADDR` | – | `:8443` | Adresse der mTLS-Agent-API. Leer: aus. Braucht `SERMON_DATABASE_URL` (die Agent-CA liegt in der Datenbank). |
| `SERMON_AGENT_HOSTNAMES` | – | `<SERMON_DOMAIN>` | DNS-Namen oder IPs, unter denen Agents die Agent-API erreichen (Komma); sie stehen im Server-Zertifikat. Pflicht mit `SERMON_AGENT_ADDR`. Ein Name in `agent_url` der Agents, der hier fehlt, scheitert an TLS. |
| `SERMON_AGENT_DOWNLOADS_DIR` | – | `/srv/downloads` (`./downloads`) | Signierte Binaries von Agent und Helper für `install.sh` und Self-Updates (Kanal `stable` direkt darin, `beta` in `beta/`). |
| `SERMON_REPO_DIR` | – | `/srv/repo` (`./repo`) | Signierte Paket-Repos für `install.sh --repo` unter `/repo/`. |

## Logs

| Variable | ab Werk | compose | Bedeutung |
|---|---|---|---|
| `SERMON_LOKI_URL` | – | `http://loki:3100` | Loki für die Logs der Nodes ([ADR 0031](../../adr/0031-logs-in-loki.md)); `http(s)://` ohne Zugangsdaten, Query oder Fragment. Leer: der Server nimmt keine Logs an und warnt beim Start. |

## Fristen

Dauern im Format von Go (`720h`, `90m`); Tage gibt es nicht. Die Jobs laufen stündlich.

| Variable | ab Werk | compose | Bedeutung |
|---|---|---|---|
| `SERMON_ORG_DELETION_GRACE` | `720h` | aus `.env` | Zwischen „Zum Löschen vormerken“ und dem Löschen einer Organisation; mindestens `168h`. |
| `SERMON_NODE_DELETION_GRACE` | `168h` | aus `.env` | Zwischen „Node löschen“ und dem Löschen seiner Daten; mindestens `24h`. |
| `SERMON_ACCOUNT_DELETION_GRACE` | `720h` | aus `.env` | Konten ohne Mitgliedschaft werden danach gelöscht (Superadmins nie); mindestens `24h`. |
| `SERMON_AUDIT_RETENTION` | `17520h` | aus `.env` | So lange behalten die Audit-Logs der Organisationen und der Instanz einen Eintrag; `2160h` bis `87600h`. |

## Mail (SMTP)

In Produktion Pflicht, sobald eine Datenbank angebunden ist: Einladungen gehen nur per Mail hinaus, damit die Adresse
bewiesen ist.

| Variable | ab Werk | compose | Bedeutung |
|---|---|---|---|
| `SERMON_SMTP_HOST` | – | aus `.env` | Name oder IP des Mailservers. |
| `SERMON_SMTP_TLS` | `starttls` | `starttls` | `starttls` (STARTTLS Pflicht), `tls` (TLS ab dem ersten Byte) oder `none` (nur mit `SERMON_MODE=development`). |
| `SERMON_SMTP_PORT` | `587` | `587` | Ab Werk `587`, mit `tls` `465`, mit `none` `25`. |
| `SERMON_SMTP_FROM` | – | aus `.env` | Absender, z. B. `SerMon <sermon@example.org>`; Pflicht. |
| `SERMON_SMTP_USERNAME` | – | aus `.env` | Leer: ohne Anmeldung. Nur zusammen mit dem Passwort. |
| `SERMON_SMTP_PASSWORD` | – | `secrets/smtp_password` | Passwort zu `SERMON_SMTP_USERNAME`. |
| `SERMON_SMTP_CA_FILE` | – | nicht gesetzt | PEM-Datei mit den CAs, gegen die der Mailserver statt der System-CAs geprüft wird. |

## Anmeldung über OIDC

Ohne `SERMON_OIDC_ISSUER` gibt es nur lokale Konten; die übrigen Variablen sind dann ein Fehler, wenn sie einen Wert
haben. Einrichtung: [Installation](../installation.md).

| Variable | ab Werk | compose | Bedeutung |
|---|---|---|---|
| `SERMON_OIDC_ISSUER` | – | aus `.env` | Issuer-URL des Providers; in Produktion nur `https://`. |
| `SERMON_OIDC_CLIENT_ID` | – | aus `.env` | Client-ID (höchstens 255 Bytes); Pflicht. |
| `SERMON_OIDC_CLIENT_SECRET` | – | `secrets/oidc_client_secret` | Client-Secret; Pflicht. |
| `SERMON_OIDC_NAME` | `SSO` | aus `.env` | Beschriftung des Knopfs auf der Anmeldeseite (höchstens 40 Zeichen). |
| `SERMON_OIDC_SCOPES` | `email profile` | aus `.env` | Scopes zusätzlich zu `openid`; `groups` für die Zuordnung von Gruppen. |
| `SERMON_OIDC_MFA` | `amr` | aus `.env` | Wann die Anmeldung beim Provider als zwei Faktoren gilt: `amr` (der Claim nennt einen zweiten Faktor), `trusted` (immer – nur, wenn der Provider ihn erzwingt) oder `never`. |
| `SERMON_OIDC_REDIRECT_URL` | erster Origin + `/auth/oidc/callback` | nicht gesetzt | Rücksprungadresse; muss auf `/auth/oidc/callback` enden. |
| `SERMON_OIDC_JIT` | `true` | aus `.env` | Konten und Mitgliedschaften nach den Gruppen-Zuordnungen anlegen; `false` schaltet das ab. |

## Nur für Backup und Restore

Diese Variablen liest nur `sermon-server backup` bzw. `restore` ([ADR 0042](../../adr/0042-logisches-backup-und-restore.md),
[Backup und Restore](../backup.md)); der Server selbst nie. Dazu kommen `SERMON_MASTER_KEYS` und beim Restore
`SERMON_DATABASE_MIGRATE_URL`.

| Variable | ab Werk | compose | Bedeutung |
|---|---|---|---|
| `SERMON_DATABASE_BACKUP_URL` | – | `secrets/database_backup_url`, nur im Service `backup` | Verbindung als `sermon_backup` (nur lesend, `BYPASSRLS`). |
| `SERMON_BACKUP_RECIPIENTS` | – | `secrets/backup_recipients`, nur im Service `backup` | Öffentliche age-Schlüssel, einer je Zeile (`sermon-server backup-keygen`). |
| `SERMON_BACKUP_IDENTITY` | – | `secrets/backup_identity`, nur für den Restore | Geheimer age-Schlüssel. Liegt nie dauerhaft auf dem Server. |

## Werte außerhalb des Servers

- **Beim Bauen:** `SERMON_RELEASE_KEY` (öffentlicher Release-Schlüssel, den `install.sh` prüft) und `SERMON_VERSION`
  werden in die Binary einkompiliert, sie sind keine Laufzeit-Variablen.
- **`.env` der Compose:** `SERMON_DOMAIN`, `SERMON_ACME_EMAIL`, `SERMON_AGENT_PORT`, `SERMON_PROXY_IP`,
  `SERMON_EDGE_SUBNET`, `SERMON_EDGE_IP_RANGE`, `SERMON_LOKI_MEMORY`, `SERMON_LOKI_GOMEMLIMIT` steuern die Compose, nicht
  den Server ([Installation](../installation.md), `deploy/compose/env.example`).
- **Auf den Hosts:** `SERMON_ENROLLMENT_TOKEN` trägt das Enrollment-Token zu `install.sh` und `sermon-agent enroll`.
  Alles Übrige des Agents steht in `/etc/sermon/agent.toml` und `/etc/sermon/helper.toml` ([Härtung](hardening.md)).
