# 0028 – Produktions-Compose: Agent-Port direkt, Server-Image aus dem Quellcode

- Status: accepted
- Datum: 2026-11-22
- Bezug: KONZEPT.md §18, §22; ADR 0006, ADR 0007, ADR 0009, ADR 0012, ADR 0027; Threat Model TB1b, T4, T73;
  IMPLEMENTIERUNGSPLAN M2-26

## Kontext

Für eine echte Installation fehlte eine Compose-Datei. KONZEPT §22 sah Caddy als einzigen öffentlichen Eingang vor,
der den Agent-Port per TLS-Passthrough an den Server weiterreicht (mTLS endet im Server). Caddy kann das nur mit dem
Layer-4-Plugin `caddy-l4`, also mit einem eigenen, per `xcaddy` gebauten Image.

Beim Passthrough sieht der Server aber nur noch die Adresse von Caddy. Damit verlieren die Adressen in den
Agent-Ereignissen ihren Sinn, und die Erkennung „Rotation von neuer Adresse“ (M2-24, T4) schlägt nie an. Abhilfe wäre
das PROXY-Protokoll im Agent-Listener des Servers: zusätzlicher Parser vor dem TLS-Handshake, eine weitere Liste
vertrauenswürdiger Absender.

Veröffentlichte Images des Servers gibt es noch nicht; der Release-Prozess folgt mit v1.0 (T51).

## Entscheidung

- **Der Agent-Port wird direkt veröffentlicht** (`${SERMON_AGENT_PORT:-8443}:8443` am Server-Container). Docker leitet
  per DNAT weiter, der Server sieht die echte Adresse des Agents. Caddy steht nur vor der Web-UI und dem Enrollment (80
  und 443, Zertifikat per ACME). Rückfrage beim Nutzer: „Direct port“.
- **Der Server wird aus dem Quellcode gebaut** (`deploy/compose/server.Dockerfile`, Kontext ist das Repository):
  Web-UI mit Node und pnpm, statische Binary mit Go, Laufzeit in `distroless/static` als `nonroot`. Alle Basis-Images
  sind per Digest gepinnt, ohne `# syntax=`-Zeile (die zöge ein ungepinntes Frontend-Image). Den öffentlichen
  Release-Schlüssel für `/downloads` bekommt der Build als Argument (`SERMON_RELEASE_KEY`).
- **Jeder Container ist gehärtet:** `read_only`, `cap_drop: [ALL]`, `no-new-privileges`, nicht privilegiert, kein
  Docker-Socket, kein Host-Netz. Nötige Capabilities einzeln zurück: Caddy `NET_BIND_SERVICE`, PostgreSQL die fünf
  für den Wechsel zum Benutzer `postgres`. `scripts/check-compose.sh --hardened` prüft das in `just lint-compose`.
- **Netze:** `edge` (Caddy und Server: veröffentlichte Ports, ausgehend ACME, SMTP, Kanäle) und `internal`
  (`internal: true`, Server und Datenbank). Caddy hat eine feste Adresse; nur sie darf `X-Forwarded-For` setzen
  (`SERMON_TRUSTED_PROXIES` mit `/32`).
- **Secrets als Dateien** in `deploy/compose/secrets/` (Verzeichnis `0700`), angelegt von `secrets.sh`, das nie
  überschreibt. Einstellungen ohne Geheimnis stehen in `.env` (Vorlage `env.example`).
- **Erstinstallation wie im Konzept:** `docker compose run --rm migrate init …` einmal, danach `docker compose up -d`;
  Migrationen laufen beim Start im Service `migrate` (seit M2-36, siehe Nachtrag).
- **Die E2E-Demo läuft gegen diese Datei:** `deploy/e2e/compose.yaml` ist ein Overlay (Mailpit, Caddys eigene CA statt
  ACME, Agent-Hosts, Ports nur auf 127.0.0.1) und wird zusammen mit der Produktionsdatei geladen.
- Loki fehlt, bis der Server Logs schreibt (E3.2).

## Konsequenzen

- Positiv: echte Agent-Adressen ohne neuen Parser; kein eigenes Caddy-Image, keine neue Abhängigkeit; jede Änderung
  an der Produktionsdatei läuft durch die Demo in CI (amd64) und Nightly (arm64).
- Negativ: Zwei öffentliche Ports (443 und 8443) statt einem; Firewalls müssen 8443 freigeben. Die mTLS-Prüfung liegt
  ohnehin im Server, an der Angriffsfläche ändert das nichts.
- Negativ: Der Build aus dem Quellcode braucht einige Minuten und Netz (npm, Go-Proxy). Veröffentlichte, signierte
  Images folgen mit dem Release-Prozess (v1.0); dann zeigt `image:` auf sie.
- Wer Agents unbedingt über 443 erreichen will, braucht einen Layer-4-Proxy mit PROXY-Protokoll; das wäre ein neues
  ADR.

## Nachtrag M2-36: Owner-Rolle und Migrationen ohne Superuser

Befund des Security-Reviews M2-28 (T73): Der Server-Container hatte dauerhaft die Migrations-URL mit dem Passwort des
Superusers (`POSTGRES_USER` des Images); ein übernommener Server hätte damit alle Mandanten ohne RLS gelesen und im
Datenbank-Container Befehle ausführen können (`COPY … TO PROGRAM`).

- **Drei Rollen** legt `initdb/010-roles.sh` beim ersten Start an: `sermon_owner` (NOSUPERUSER, NOBYPASSRLS, Eigentümer
  der Datenbank und damit des Schemas `public`, Mitglied von `sermon_definer`), `sermon_definer` (ohne Login) und
  `sermon_app`. Der Superuser heißt `postgres` und bleibt im Datenbank-Container.
- **Ein einmaliger Service `migrate`** (dasselbe Image, `sermon-server migrate`, `restart: "no"`) migriert als
  `sermon_owner`; der Server startet erst nach seinem Erfolg (`service_completed_successfully`) und bekommt nur
  `database_url`. `init` läuft ebenfalls dort.
- **`check-compose --hardened`** lehnt die Migrations-URL in einem dauerhaft laufenden Container und das Passwort des
  Superusers außerhalb des Datenbank-Containers ab.
- **Verworfen:** Migrationen weiter im Server, aber als `sermon_owner` – der Owner umgeht `FORCE ROW LEVEL SECURITY`
  zwar nicht, könnte aber Policies ändern; im Dauerbetrieb braucht ihn niemand.
- **Folge:** Installationen von vor M2-36 (Superuser `sermon`) setzt man neu auf (vor dem ersten Release, Anleitung).

## Nachtrag M2-39

- **`check-compose --hardened`** lehnt zusätzlich ab: Capabilities außer `CHOWN`, `DAC_OVERRIDE`, `FOWNER`, `SETGID`,
  `SETUID` und `NET_BIND_SERVICE`; `pid`, `ipc`, `uts`, `cgroup` oder `userns_mode` vom Host; `seccomp`/`apparmor:
  unconfined` und `label:disable`; Geräte; Mounts von `/run`, `/var/run` und Sockets.
- **pnpm im Server-Image** in der Version von `packageManager` aus `package.json` (Renovate pflegt nur diese Stelle);
  corepack fehlt ab Node 25.
- **Renovate** pflegt per Regex auch das nfpm-Image, die Test-Hosts in `scripts/` und die Basis-Images der E2E-Hosts.
