# Installation mit docker compose

Diese Anleitung richtet SerMon auf einem Linux-Server mit Docker ein. Grundlage sind die Dateien in
[`deploy/compose/`](../../deploy/compose/) (Entscheidungen: [ADR 0028](../adr/0028-produktions-compose.md)) und das
veröffentlichte, signierte Server-Image eines Releases (`ghcr.io/pmoscode/sermon-server`,
[ADR 0043](../adr/0043-release-prozess.md)).

## Voraussetzungen

- Linux-Server mit Docker Engine und dem Compose-Plugin (`docker compose version`).
- Richtwert für 50 Nodes: 4 vCPU, 8 GB RAM, 200 GB SSD (KONZEPT §22).
- Ein DNS-Name, der auf den Server zeigt, z. B. `sermon.example.org`. Passkeys brauchen ihn: Unter einer IP-Adresse
  oder einem Namen ohne Punkt (`https://monitor`) läuft SerMon ohne Passkeys und meldet das beim Start im Log
  (`passkeys are off`).
- Offene Ports: **80** und **443** (Web-UI, Zertifikat von Let's Encrypt) sowie **8443** (Agents, mTLS).
- Ein Mailserver mit STARTTLS (Port 587) oder TLS (Port 465). Ohne Mail geht es in Produktion nicht: Einladungen gehen
  nur per Mail raus, damit die Adresse bewiesen ist.
- `openssl` für `secrets.sh`, `git` zum Holen der Compose-Dateien. `cosign` zum Prüfen der Signatur ist optional:
  Ohne startet `use-release.sh` es als Container (gepinnt per Digest).

## Was läuft

| Dienst | Aufgabe | Erreichbar |
|---|---|---|
| `caddy` | TLS mit Zertifikat per ACME, Reverse-Proxy für die Web-UI und das Enrollment | 80, 443 |
| `server` | `sermon-server` aus dem veröffentlichten Image (per Digest); Web-API, Ingest, Alerting, Agent-Downloads und Paket-Repos seiner Version | 8443 (Agents, mTLS) |
| `migrate` | dasselbe Image, läuft vor dem Server einmal durch und migriert die Datenbank | – |
| `backup`, `restore` | dasselbe Image, nur auf Aufruf (`docker compose run`), siehe [Backup und Restore](backup.md) | – |
| `postgres` | PostgreSQL 17 mit TimescaleDB | nur im internen Netz |
| `loki` | Logs der Nodes, getrennt pro Organisation, 14 Tage ([ADR 0031](../adr/0031-logs-in-loki.md)) | nur im internen Netz |

Alle Container laufen mit schreibgeschütztem Dateisystem, ohne Capabilities (Ausnahmen: Caddy darf Port 80/443
binden, PostgreSQL zum eigenen Benutzer wechseln) und ohne neue Privilegien. Die Datenbank hängt nur an einem Netz
ohne Route nach außen, ebenso Loki: Loki hat keine eigene Anmeldung, nur der Server erreicht es und nennt bei jedem
Zugriff die Organisation. Der Agent-Port geht direkt zum Server, damit er die echten Adressen der Agents sieht.

Loki darf höchstens 1 GiB Speicher belegen (`SERMON_LOKI_MEMORY`, dazu `SERMON_LOKI_GOMEMLIMIT` mit etwa 90 % davon),
damit große Log-Abfragen der Datenbank und dem Server nichts wegnehmen. Für einige hundert Nodes reicht das; wer mehr
hat, hebt beide Werte gemeinsam an.

Die Datenbank hat drei Rollen ([ADR 0007](../adr/0007-mandantentrennung-rls.md)): Der Superuser `postgres` bleibt im
Datenbank-Container; `sermon_owner` besitzt das Schema und migriert, aber nur im Service `migrate`; der Server meldet
sich als `sermon_app` an, für die die Mandantentrennung per Row-Level-Security immer gilt. Ein übernommener Server
kommt so weder an die Daten fremder Organisationen ohne RLS noch an das Schema.

## Einrichten

Die Version wählen (Releases: <https://github.com/pmoscode/SerMon/releases>; `X.Y.Z` ist ein Release,
`X.Y.Z-rc.N` ein Kandidat für den Testbetrieb) und ihre Compose-Dateien holen:

```sh
VERSION=1.0.0
git clone --branch "v$VERSION" --depth 1 https://github.com/pmoscode/SerMon.git
cd SerMon/deploy/compose

cp env.example .env
$EDITOR .env          # SERMON_DOMAIN, SERMON_ACME_EMAIL, SMTP

./secrets.sh          # Datenbank-Passwörter und -URLs in ./secrets/
$EDITOR secrets/smtp_password   # nur mit SERMON_SMTP_USERNAME: das Passwort, ohne Zeilenumbruch

./use-release.sh "$VERSION"     # prüft die Signatur des Images, trägt seinen Digest in .env ein
docker compose pull
docker compose run --rm migrate init --master-keys-file /var/lib/sermon/master_keys
docker compose up -d
```

`use-release.sh` lässt cosign prüfen, dass `ghcr.io/pmoscode/sermon-server:VERSION` vom Release-Workflow von SerMon
auf genau dem Tag `vVERSION` signiert ist (Sigstore, ohne Schlüssel beim Admin), und schreibt nur den so geprüften
Digest als `SERMON_IMAGE_DIGEST` in `.env`. Compose startet nichts ohne ihn, und ein Tag kann den Digest nicht mehr
ändern. Wer selbst prüfen will:

```sh
cosign verify ghcr.io/pmoscode/sermon-server:$VERSION \
  --certificate-identity "https://github.com/pmoscode/SerMon/.github/workflows/release.yml@refs/tags/v$VERSION" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Das ganze Release (Binaries, Pakete, Repos, SBOM, Provenance) prüft aus dem Checkout
`../../scripts/verify-release.sh "$VERSION"` – nur mit den öffentlichen Schlüsseln aus `packaging/` (braucht `openssl`,
`gpg`, `python3`, `curl`, `cosign`, optional `slsa-verifier`).

`init` läuft im Service `migrate`, legt die Master-Key-Datei an, migriert die Datenbank, erzeugt die Agent-CA und gibt ein **Setup-Token** aus.
Es ist 30 Minuten gültig und funktioniert genau einmal. Öffnen Sie `https://<SERMON_DOMAIN>`, geben Sie das Token ein und
legen Sie die erste Organisation und den ersten Superadmin an. Ist das Token abgelaufen, schreibt der Server bei jedem
Start ein neues ins Log:

```sh
docker compose logs server | grep setup_token
```

Nach der Einrichtung verweigert `init` jede weitere Ausführung. Danach gleich die Sicherung einrichten: Backup-Schlüssel
und eine Kopie der Master-Keys ([Backup und Restore](backup.md#einmal-einrichten)).

## Agents anbinden

In der UI unter **Nodes → Node hinzufügen** entsteht ein Befehl mit Enrollment-Token. Der Agent meldet sich danach
über `<SERMON_DOMAIN>:8443`. Das Image eines Releases bringt alles für `install.sh` mit: den öffentlichen
Release-Schlüssel (einkompiliert) und die signierten Binaries von Agent und Helper seiner Version für amd64 und arm64
(`/srv/downloads`). Server und Agents haben so nach jedem `pull` dieselbe Version. Wer aus dem Quellcode baut, legt sie
selbst hin ([Aus dem Quellcode bauen](#aus-dem-quellcode-bauen)).

### Paket-Repos (`install.sh --repo`)

Statt der Binaries kann `install.sh --repo` die Pakete `sermon-agent` und `sermon-helper` aus einem signierten Repo
des Servers installieren (apt, dnf, apk); danach aktualisiert der Paketmanager sie, auch über eine Update-Policy. Der
Server liefert die Repos seiner Version unter `https://<SERMON_DOMAIN>/repo/` aus dem Image aus (`/srv/repo`). Die
öffentlichen Schlüssel liegen unter `/repo/keys/` (`sermon.asc` für apt und dnf, `sermon-<id>.rsa.pub` für apk,
benannt nach dem Fingerprint des Schlüssels) mit der Liste `SHA256SUMS`, die der Release-Schlüssel signiert.
`install.sh --repo` richtet nur Schlüssel ein, die in der signierten Liste stehen; ohne Release-Schlüssel (ein Build
aus dem Quellcode ohne `SERMON_RELEASE_KEY`) bürgt nur TLS für sie. Der Paketmanager
muss dem Zertifikat des Servers vertrauen: Mit einem selbstsignierten Zertifikat (`--ca-fingerprint`) geht `--repo`
nicht, dann die CA des Servers auf dem Host installieren.

`install.sh --repo` legt die Schlüssel dort ab, wo sie das Paket `sermon-agent` hält
(`/usr/share/keyrings/sermon-archive-keyring.asc`, `/etc/pki/rpm-gpg/RPM-GPG-KEY-sermon`,
`/etc/apk/keys/sermon-<id>.rsa.pub`); jedes Update des Agents ersetzt sie. Außerdem bekommen `sermon-agent` und
`sermon-helper` Vorrang vor gleichnamigen Paketen anderer Quellen (apt: `/etc/apt/preferences.d/sermon`, dnf:
`priority` und `includepkgs` in `/etc/yum.repos.d/sermon.repo`; apk kennt das nicht).

**Schlüsselwechsel** ([ADR 0036](../adr/0036-self-update-und-paket-repos.md)): Die öffentlichen Schlüssel des neuen
Paares liegen beim Bau in `KEYS/next/` (auch für `scripts/package-agent.sh`, `SERMON_PKG_PUBLIC_KEYS`). Dieses
Release ist noch mit den alten Schlüsseln signiert und bringt die neuen mit dem Paket auf die Hosts; das nächste
signiert nur mit den neuen, sein Paket entfernt die alten. Ein Host muss dazwischen also ein Update bekommen haben.
dnf behält einen einmal importierten Schlüssel: Soll der alte nicht mehr gelten (etwa nach einer Kompromittierung),
auf dem Host `rpm -e gpg-pubkey-<id>` und `dnf clean all`.

Wer `sermon-agent` entfernt, entfernt auch die Schlüssel; die Quelle (`/etc/apt/sources.list.d/sermon.sources`,
`/etc/yum.repos.d/sermon.repo` bzw. die Zeile in `/etc/apk/repositories`) dann ebenfalls löschen.

### Root-Helper (Updates und Reboots)

Neben dem Agent installiert `install.sh` den Root-Helper `sermon-helper` (signiert wie der Agent, Binaries
`sermon-helper-linux-amd64` und `…-arm64` im Image; als Paket `sermon-helper`). Er führt für den
Agent Paket-Updates und Reboots aus, nur feste Operationen und nur für den User `sermon`. Was der Host erlaubt, steht
in `/etc/sermon/helper.toml`; der Server kann es nicht ändern:

| Schlüssel | ab Werk | Schalter von `install.sh` |
|---|---|---|
| `allow_remote_updates` | an | `--no-remote-updates` |
| `allow_remote_reboot` | **aus** | `--allow-remote-reboot` |
| `allow_self_update` | an | `--no-self-update` |
| `allow_prerelease` | aus | – (von Hand in der Datei) |
| `allow_smart` | an | `--no-smart` |

`allow_smart` erlaubt dem Modul `smart`, über den Helper die SMART-Daten der Disks zu lesen (`smartctl`, nur lesend;
das Paket `smartmontools` installiert der Admin). Fehlt der Schlüssel in einer älteren `helper.toml`, gilt nach dem
Update von Agent und Helper ebenfalls „an“; wer das nicht will, setzt `allow_smart = false` vor dem Update (oder ruft
`install.sh --no-smart` auf). `allow_prerelease` lässt ein Self-Update auf eine Vorabversion
(Kanal `beta`) zu, solange der Host noch keine hat. Ein Helper, den `install.sh` eingerichtet hat, erneuert sich mit
dem Agent; ein Helper aus der Zeit davor nicht – die Updates-Seite des Nodes sagt dann, dass `install.sh` einmal
erneut laufen sollte.

Ohne Schalter bleibt eine vorhandene `helper.toml` unverändert. `--no-helper` installiert keinen Helper bzw. schaltet
einen vorhandenen ab; dann gibt es keine Fernsteuerung. Ein abgeschalteter Helper (`systemctl disable --now
sermon-helper.socket`) bleibt auch nach Updates aus. `sermon-agent helper status` (als `sermon`) zeigt, was der Helper
erlaubt.

### Kanal `beta`

Agents in Gruppen mit dem Kanal `beta` ([ADR 0036](../adr/0036-self-update-und-paket-repos.md)) bekommen per
Self-Update eine neuere Version als der Server, etwa einen Kandidaten. Dafür die signierten Binaries dieses Releases
(`sermon-agent-linux-*`, `sermon-helper-linux-*` mit `.minisig` von seiner Release-Seite) nach `deploy/compose/beta/`
legen und in `compose.override.yaml` neben `compose.yaml` einhängen:

```yaml
services:
  server:
    volumes: ["./beta:/srv/downloads/beta:ro"]
```

Der Server liest das Verzeichnis alle 5 Minuten und bietet nur Dateien mit gültiger Signatur an.

## Was wo liegt

| Was | Wo | Hinweis |
|---|---|---|
| Einstellungen | `deploy/compose/.env` | keine Geheimnisse; `SERMON_IMAGE_DIGEST` setzt `use-release.sh` |
| Server-Image | `ghcr.io/pmoscode/sermon-server@sha256:…` | mit den signierten Agent-Binaries und Paket-Repos seiner Version |
| Datenbank-Passwörter, SMTP-Passwort | `deploy/compose/secrets/` | `secrets.sh` überschreibt nie; ein neues Passwort sperrt den Server aus. `postgres_password` (Superuser) braucht nur der Datenbank-Container |
| Master-Keys | Volume `sermon_server-state` | **getrennt sichern** ([Backup](backup.md)): ohne Master-Keys sind alle versiegelten Secrets verloren |
| Datenbank | Volume `sermon_postgres-data` | |
| Logs | Volume `sermon_loki-data` | 14 Tage; Konfiguration in `deploy/compose/loki.yaml` |
| Zertifikate von Caddy | Volume `sermon_caddy-data` | |

Die Logs der Container sind auf 5 × 10 MB begrenzt.

## Aktualisieren

Erst die Release-Notes der neuen Version lesen (Abschnitt „Upgrade“: Breaking Changes, neue Secrets, Daten, die das
Upgrade löscht), dann:

```sh
docker compose run --rm -T backup > backups/vor-dem-update.backup   # Backup und Restore: backup.md
git fetch --depth 1 origin tag "v$VERSION" && git checkout "v$VERSION"   # die Compose-Dateien der neuen Version
./secrets.sh                      # legt neue Secret-Dateien an (z. B. oidc_client_secret), vorhandene bleiben
./use-release.sh "$VERSION"       # prüft die Signatur, setzt den neuen Digest
docker compose pull
docker compose up -d
```

Die Migrationen spielt der Service `migrate` ein, bevor der Server startet; schlägt das fehl, startet der Server nicht
(`docker compose logs migrate`). Agents mit Self-Update ziehen danach nach (Update-Policy der Gruppe), Hosts mit dem
Paket-Repo über ihren Paketmanager.

**Zurück:** `use-release.sh` notiert in `.env` den vorigen Digest (`before: sha256:…`). Migrationen laufen nur vorwärts:
Ein Rollback ist das vorige Image mit dem Backup von vor dem Update ([Restore](backup.md)), nicht nur der alte Digest.

## Aus dem Quellcode bauen

Für Entwicklung und Tests kann der Server statt aus dem veröffentlichten Image aus dem Checkout gebaut werden
(Overlay [`source.yaml`](../../deploy/compose/source.yaml)). In `.env`:

```sh
COMPOSE_FILE=compose.yaml:source.yaml
SERMON_IMAGE_DIGEST=source          # wird nicht genutzt, compose.yaml verlangt einen Wert
SERMON_RELEASE_KEY=…                # öffentlicher Release-Schlüssel (scripts/release-sign.sh pubkey KEY.pem)
```

dann `docker compose build` statt `use-release.sh` und `pull`. Ein solches Image enthält keine Agent-Binaries und
Repos; `source.yaml` hängt dafür `deploy/compose/downloads/` und `deploy/compose/repo/` ein:

- Binaries von Agent und Helper in der Version des Servers, signiert mit dem Release-Schlüssel:
  `just sign-agent KEY.pem VERSION` (nach `dist/agent/`, von dort nach `downloads/`).
- Repos: `scripts/build-repos.sh dist/packages KEYS deploy/compose/repo` aus den Paketen von
  `scripts/package-agent.sh` (`KEYS`: `gpg.asc`, `gpg.pub.asc`, `apk.rsa`, `apk.rsa.pub`), danach
  `scripts/release-sign.sh sign KEY.pem --version VERSION deploy/compose/repo/keys/SHA256SUMS`.

## Self-Monitoring

Mit `SERMON_DATABASE_SIZE_LIMIT` (z. B. `150GiB`) warnt der Server die Superadmins per Mail und in der UI, sobald die
Datenbank 80 % davon erreicht ([ADR 0027](../adr/0027-server-ereignisse-und-self-monitoring.md)). Der Server kennt nur
die Größe der Datenbank, nicht den freien Platz: Setzen Sie die Grenze unter den Platz des Datenträgers. Den Füllstand
der Volumes misst ein Agent auf dem Server-Host.

Wie viel CPU, Speicher und Platz eine Installation unter Last braucht, misst `scripts/load-report.sh` – auch im
laufenden Betrieb, siehe [Lasttest und Performance-Nachweis](de/load-test.md).

## Länder im Security-Dashboard (optional)

Der Server kann zu den Adressen im Security-Dashboard das Land zeigen. Er lädt dafür nichts herunter: Legen Sie eine
Datenbank im MaxMind-DB-Format nach `deploy/compose/geoip/`, etwa „IP to Country Lite“ von DB-IP
(<https://db-ip.com/db/lite.php>, CC BY 4.0) oder GeoLite2 Country von MaxMind (Konto nötig), und setzen Sie in `.env`
`SERMON_GEOIP_DB_FILE=/srv/geoip/<datei>.mmdb`. Den Lizenzhinweis zeigt das Dashboard neben den Ländern; er ergibt
sich aus der Datei, `SERMON_GEOIP_ATTRIBUTION` ersetzt ihn. Private Adressen haben kein Land. Die Datei liest der Server
beim Start; ist sie älter als 90 Tage, warnt er im Log. Zum Aktualisieren die Datei ersetzen und den Server neu
starten (`docker compose restart server`).

## Anmeldung über OIDC (optional)

SerMon kann Anmeldungen eines OpenID-Connect-Providers annehmen (Keycloak, Authentik, Entra ID, Dex …,
[ADR 0033](../adr/0033-anmeldung-ueber-oidc.md)). Legen Sie beim Provider einen vertraulichen Client an mit der
Redirect-URI `https://<SERMON_DOMAIN>/auth/oidc/callback` und den Scopes `openid email profile`. Dann in `.env`:

```sh
SERMON_OIDC_ISSUER=https://id.example.com/realms/ops   # die Issuer-URL des Providers
SERMON_OIDC_CLIENT_ID=sermon
SERMON_OIDC_NAME=Firmen-Login                          # Beschriftung des Knopfs, Standard „SSO“
```

und das Client-Secret ohne Zeilenumbruch nach `secrets/oidc_client_secret`. Der Server spricht den Provider über
denselben abgesicherten Client wie die Benachrichtigungskanäle an: Liegt er im internen Netz, braucht seine Adresse
einen Eintrag in `SERMON_OUTBOUND_ALLOWLIST`.

- Angemeldet wird über die Identität beim Provider (Issuer und Subject), nie über die E-Mail. Ein User verknüpft sie
  unter „Kontosicherheit“, angemeldet und nach erneuter Bestätigung, und löst sie dort wieder, solange ihm ein Passwort
  bleibt. Die Anmeldeseite zeigt den Knopf „Mit <SERMON_OIDC_NAME> anmelden“.
- **2FA-Pflicht einer Organisation:** `SERMON_OIDC_MFA=amr` (Standard) erkennt einen zweiten Faktor am Claim `amr`
  des ID-Tokens. Viele Provider setzen ihn nicht; erzwingt Ihr Provider selbst einen zweiten Faktor, setzen Sie
  `trusted`. Mit `never` gilt immer der zweite Faktor von SerMon.
- **Gruppen:** Die Admins einer Organisation ordnen Gruppen des Providers Rollen zu (Seite „Organisation“, Reiter „SSO-Gruppen“). Dafür
  muss das ID-Token den Claim `groups` tragen: `SERMON_OIDC_SCOPES=email profile groups` (bei manchen Providern heißt
  der Scope anders oder es braucht einen Mapper). Bei jeder Anmeldung folgen die Rollen aus Zuordnungen den Gruppen;
  von Hand vergebene Rollen bleiben.
- **Konten anlegen (JIT):** Meldet sich jemand an, den SerMon noch nicht kennt, und passt eine Zuordnung, legt SerMon
  Konto und Mitgliedschaft an – nur mit bestätigter E-Mail und nur, wenn noch kein Konto diese E-Mail hat (dann muss
  die Identität dort verknüpft werden). `SERMON_OIDC_JIT=false` schaltet das ab. Bedenken Sie: Mit JIT kann der Admin
  einer Organisation jedes Mitglied einer Provider-Gruppe in seine Organisation aufnehmen und sieht dessen E-Mail.

## Organisationen löschen

Eine zum Löschen vorgemerkte Organisation bleibt gesperrt, bis die Frist abläuft; danach löscht der Server sie mit
allen Daten (Prüfung stündlich, [ADR 0029](../adr/0029-organisationen-verwalten-sperren-loeschen.md)). Die Frist
setzt `SERMON_ORG_DELETION_GRACE` (Standard `720h` = 30 Tage, mindestens `168h` = 7 Tage). Wer das Audit-Log einer
Organisation aufbewahren muss, lädt es während der Sperre auf der Seite „Organisationen“ herunter.

## Nodes löschen

Ein gelöschter Node bleibt deaktiviert, bis seine Frist abläuft; danach löscht der Server ihn mit Metriken, Events,
Alerts und Logs (Prüfung stündlich, [ADR 0041](../adr/0041-nodes-deaktivieren-loeschen.md)). Die Frist setzt
`SERMON_NODE_DELETION_GRACE` (Standard `168h` = 7 Tage, mindestens `24h`).

## Konten und Audit-Logs

Konten ohne Mitgliedschaft löscht der Server nach `SERMON_ACCOUNT_DELETION_GRACE` (Standard `720h` = 30 Tage,
mindestens `24h`; Superadmins nie), abgelaufene Einladungen sofort; beide Audit-Logs behalten Einträge
`SERMON_AUDIT_RETENTION` lang (Standard `17520h` = 2 Jahre, `2160h` bis `87600h`). Security-Events und Verlauf der
Alerts behalten Organisationen ohne eigene Einstellung `SERMON_EVENT_RETENTION` lang (Standard `2160h` = 90 Tage,
ganze Tage von `168h` bis `8760h`). Prüfung stündlich. Auf Antrag
löscht ein Superadmin ein Konto auf der Seite „Organisationen“. Mehr in der
[Vorlage zur DSGVO-Dokumentation](../privacy/de/README.md).

## Wenn die Standardnetze kollidieren

Das Netz `edge` nutzt `172.30.78.0/24`, Caddy hat darin die feste Adresse `172.30.78.10`, dynamische Adressen kommen
aus `172.30.78.128/25`. Kollidiert das mit einem vorhandenen Netz, setzen Sie in `.env` `SERMON_EDGE_SUBNET`,
`SERMON_EDGE_IP_RANGE` und `SERMON_PROXY_IP` passend; die Adresse von Caddy muss außerhalb des dynamischen Bereichs
liegen. Nur sie darf dem Server die Adresse des Browsers nennen (`X-Forwarded-For`).

Läuft auf dem Host schon ein anderer Reverse-Proxy (Traefik, nginx), ersetzt er Caddy, siehe
[Härtung: hinter einem anderen Reverse-Proxy](de/hardening.md#hinter-einem-anderen-reverse-proxy).

## Installationen vor M2-36

Bis M2-36 migrierte der Server selbst, als Superuser `sermon`. Solche Installationen (vor dem ersten Release, nur zum
Ausprobieren) bitte neu aufsetzen: `docker compose down -v`, `./secrets/` löschen und die Schritte unter
[Einrichten](#einrichten) wiederholen. Daten gehen dabei verloren.
