# Härtung

SerMon ist ab Werk streng eingestellt. Diese Seite zeigt, was die Voreinstellungen schützen, wo Sie sie für einen Host
oder die Instanz weiter einschränken – und was ein übernommener Server trotzdem kann. Grundlage sind das
[Threat Model](../../security/threat-model.md) (Grenzen TB1–TB9, Bedrohungen T…) und KONZEPT §18.

## Auf den Hosts

### Was der Agent darf

- `sermon-agent` läuft als User `sermon` ohne Capabilities, in den Gruppen `systemd-journal` und `adm` (Journal und
  Logdateien lesen). Er führt nur typisierte Befehle aus, es gibt kein „exec“ (T12).
- Seine Konfiguration `/etc/sermon/agent.toml` gehört `root:sermon` mit `0640` (Verzeichnis `0750`). Der Agent kann
  sie nur lesen (`ReadOnlyPaths=/etc/sermon`): Ein übernommener Agent lockert seine Vetos nicht.
- Identität und Puffer liegen in `/var/lib/sermon-agent` (`sermon`, `0750`; der Schlüssel `0600`). Der Puffer hält bis
  zu 24 Stunden bzw. 200 MiB, wenn der Server nicht erreichbar ist.
- Root-Rechte hat nur der optionale Root-Helper `sermon-helper` – mit festen Operationen und einem eigenen Veto
  ([unten](#root-helper-helpertoml)).

### Lokale Vetos in `agent.toml`

Der Server bestimmt, was ein Agent tut; der Host bestimmt in `[local]` von `/etc/sermon/agent.toml`, was davon erlaubt
ist. Der Server kann diese Werte weder sehen noch ändern.

| Schlüssel | ab Werk | Wirkung |
|---|---|---|
| `allow_live_view` | `true` | Die Live-Ansicht der Prozesse darf gestartet werden (nur auf Anfrage, Befehlszeilen redigiert). |
| `allow_logs` | `true` | Log-Zeilen gehen an den Server. Security-Events (SSH, sudo, Konten, Neustarts) kommen auch mit `false`. |
| `log_file_prefixes` | `["/var/log"]` | Unter diesen Verzeichnissen dürfen die Logdatei-Muster der Organisation lesen. Nie: das State-Verzeichnis, `/etc/sermon`, `/proc`, `/sys`, `/dev`, die Dateien des Journals, `wtmp`, `btmp`, `lastlog` (T81). |
| `allow_modules` | `true` | `false`: kein Modul läuft. |
| `module_targets` | `["127.0.0.0/8", "::1/128"]` | Netze (CIDR) oder Adressen, zu denen Module sich verbinden (T107). |
| `module_hosts` | `["localhost"]` | Namen, die Module auflösen dürfen: ein Name, `*.example.org` (alle Namen darunter) oder `*`. Andere Namen gehen nie an den Resolver; jede Adresse, auf die ein Name zeigt, muss in `module_targets` liegen. |
| `module_ports` | `[80, 443, 465, 636, 993, 995, 3306, 5432, 6379, 8080, 8443, "9090-9999"]` | TCP-Ports, zu denen Module sich verbinden; Bereiche als Text (`"9090-9999"`). |
| `module_sockets` | `["/run", "/var/run"]` | Unix-Sockets unter diesen Verzeichnissen; nie in einem Verzeichnis, in das jeder schreiben darf (`/run/lock`, `/tmp`). |
| `module_file_prefixes` | `["/etc/ssl", "/etc/pki", "/etc/letsencrypt"]` | Dateien, die Module lesen (Zertifikate, CA-Dateien, T108). |
| `allow_docker_socket` | `false` | Sockets von Container-Runtimes und VM-Managern (Docker, Podman, containerd, CRI-O, BuildKit, libvirt, LXD/Incus). Wer sie benutzt, ist root auf dem Host (O4). |

Gut zu wissen:

- Eine Liste **ersetzt** die Voreinstellung: Wer `module_ports = [5432]` setzt, erlaubt nur noch 5432.
- Pfade sind absolut, ohne `.` und `..`, und werden segmentweise verglichen. Symlinks werden aufgelöst und das Ziel
  erneut geprüft: `/etc/letsencrypt/live` als Präfix reicht nicht, weil die Dateien dort auf `../archive/…` zeigen.
- Für Module immer gesperrt, egal was die Präfixe sagen: `/etc/sermon`, `/proc`, `/sys`, `/dev`,
  `/var/lib/sermon-agent`, `/run/sermon` (der Socket des Helpers), das eingestellte State-Verzeichnis und das
  Verzeichnis der `agent.toml`.
- Ein unbekannter Schlüssel oder ein ungültiger Wert hält den Agent an (`invalid configuration: …` im Log) – ein
  Tippfehler lockert also nie still ein Veto.
- Der Agent liest die Datei nur beim Start: `systemctl restart sermon-agent` bzw. `rc-service sermon-agent restart`.
- Weigert sich ein Modul, steht auf der Node-Seite „Vom Host verboten“; der Grund nennt nur, was der Server selbst
  geschickt hat, nie aufgelöste Adressen oder Pfade.

Ein enger Host, der nur nginx-Logs sendet, keine Live-Ansicht erlaubt und ein Modul nur zu PostgreSQL lässt:

```toml
[local]
allow_live_view = false
log_file_prefixes = ["/var/log/nginx"]
module_ports = [5432]
```

Soll ein Modul einen Dienst auf einem anderen Host prüfen, Netz und Namen gezielt freigeben, nicht `*`:

```toml
[local]
module_targets = ["127.0.0.0/8", "::1/128", "192.0.2.10/32"]
module_hosts = ["localhost", "db.example.org"]
```

### Root-Helper (`helper.toml`)

Der Root-Helper nimmt nur feste Operationen an (Updates auflisten und installieren, Neustart, Self-Update des Agents,
SMART lesen), ohne freie Argumente, und nur vom User `sermon` (Socket `/run/sermon/helper.sock`, `root:sermon 0660`,
geprüft über `SO_PEERCRED`; T38, T39). Was der Host erlaubt, steht in `/etc/sermon/helper.toml`:

| Schlüssel | ab Werk | Schalter von `install.sh` | Wirkung |
|---|---|---|---|
| `allow_remote_updates` | `true` | `--no-remote-updates` | Updates aus den signierten Quellen der Distribution installieren. |
| `allow_remote_reboot` | `false` | `--allow-remote-reboot` | Auf Anfrage neu starten (nötig für „Neustart nach Updates“). Aus, weil ein Neustart der ganzen Flotte das Schlimmste wäre, was ein übernommener Server tun kann. |
| `allow_self_update` | `true` | `--no-self-update` | Den Agent durch eine neuere, mit dem Release-Schlüssel signierte Version ersetzen. |
| `allow_prerelease` | `false` | – (von Hand) | Self-Update auf eine Vorabversion (Kanal `beta`), solange der Host noch keine hat. |
| `allow_smart` | `true` | `--no-smart` | SMART-Daten der Disks mit festem, nur lesendem `smartctl` (Modul `smart`). |

- Datei und Verzeichnis müssen `root` gehören und dürfen für Gruppe und andere nicht schreibbar sein; sonst lehnt der
  Helper jede Anfrage ab (fail closed). Er liest die Datei bei **jeder** Anfrage neu – eine Änderung im Vorfall gilt
  sofort, ohne Neustart (T103).
- Ohne Helper (`install.sh --no-helper`, oder das Paket `sermon-helper` nicht installiert) gibt es keine
  Fernsteuerung: keine Updates, Neustarts, Self-Updates und kein SMART.
- `sudo -u sermon sermon-agent helper status` zeigt Version, Paketmanager und was der Helper erlaubt.
- Jede Anfrage, auch eine abgelehnte, steht im Journal des Helpers; jeder Lauf auch im Audit-Log (T41).

### Optionen von `install.sh`

| Option | Wirkung |
|---|---|
| `--server` | Öffentliche HTTPS-Adresse des Servers (wie im Browser); Pflicht. |
| `--token-file` | Datei mit dem Enrollment-Token. Sonst aus `SERMON_ENROLLMENT_TOKEN`; `--token` geht auch, steht dann aber in der Prozessliste (das Skript warnt). |
| `--ca-fingerprint` | SHA-256 des selbstsignierten Server-Zertifikats bzw. einer CA, die der Server mitschickt (`sha256:AB:CD:…`). |
| `--agent-url` | mTLS-Agent-API, falls nicht `<Host von --server>:8443`. |
| `--force` | Eine vorhandene Anmeldung bzw. eine `agent.toml`, die auf einen anderen Server zeigt, ersetzen. Ohne den Schalter schickt das Skript das Token nie an einen anderen Server als den der vorhandenen Konfiguration (T58). |
| `--repo` | Die Pakete aus dem signierten Repo des Servers installieren (apt, dnf, apk); danach aktualisiert sie der Paketmanager. Braucht ein Zertifikat, dem der Paketmanager vertraut. |
| `--grant-log-access` | Alpine ohne journald: busybox syslogd schreibt mit der Gruppe `sermon` statt `wheel` (T70). |
| `--no-helper` | Keinen Helper installieren bzw. einen vorhandenen abschalten. |
| `--no-remote-updates` | `allow_remote_updates = false` |
| `--allow-remote-reboot` | `allow_remote_reboot = true` |
| `--no-self-update` | `allow_self_update = false` |
| `--no-smart` | `allow_smart = false` |

Ohne Schalter für den Helper bleibt eine vorhandene `helper.toml` unverändert. Das Skript installiert nur Binaries, die
mit dem eingebauten Release-Schlüssel und für genau die Version des Servers signiert sind (T48). Es bleibt `curl | sh`:
Wer den Server übernimmt, kann bei der **nächsten** Installation ein anderes Skript ausliefern (T50). Dagegen helfen
„Skript erst ansehen“ in der UI und die Paket-Repos (`--repo`), deren Schlüssel der Paketmanager hält. Paket-Repos und
ihre Schlüssel: [Installation](../installation.md).

### systemd und OpenRC

Die Unit `sermon-agent.service` ist gehärtet; `systemd-analyze security sermon-agent` soll unter 2,0 bleiben
(KONZEPT §18.4). Sie setzt unter anderem:

- `User=sermon`, leere `CapabilityBoundingSet=` und `AmbientCapabilities=`, `NoNewPrivileges=yes`;
- `ProtectSystem=strict`, `ProtectHome=read-only` (der Dateisystem-Collector sieht `/home`), `PrivateTmp=yes`,
  `PrivateDevices=yes`, `ReadWritePaths=/var/lib/sermon-agent`, `ReadOnlyPaths=/etc/sermon`;
- `RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK`, `SystemCallFilter=@system-service` ohne
  `@privileged @resources` (nur `adjtimex` und `clock_adjtime` zum Lesen der Uhr), `MemoryDenyWriteExecute=yes`;
- Grenzen `MemoryMax=128M`, `CPUQuota=20%`, `TasksMax=64` und `LimitCORE=0` (keine Core-Dumps: der Speicher hält die
  Zugangsdaten der Module).

Der Helper (`sermon-helper.service`) startet über `sermon-helper.socket` bei der ersten Anfrage und endet nach einer
Minute ohne Anfrage. Er läuft als root mit `ProtectSystem=full`, `ProtectHome=yes`, `NoNewPrivileges=yes`, Geräten nur
lesend für die Disks (`DeviceAllow=block-sd r`, `block-blkext r`), `CPUQuota=50%`, `MemoryMax=1G`. Ein Lauf (Updates,
Neustart, Self-Update) ist eine eigene Unit `sermon-helper-run-<id>` mit den Rechten, die ein Paketmanager braucht.

Ändern Sie die Units nur, um sie weiter einzuschränken (`systemctl edit sermon-agent`). Unter OpenRC startet
`supervise-daemon` den Agent als `sermon` ohne Core-Dumps (`rc_ulimit="-c 0"`); die Sandbox von systemd fehlt dort,
die Trennung beruht auf User und Dateirechten.

## Auf dem Server

### Netze und Ports

Die Compose ([`deploy/compose`](../../../deploy/compose/), [ADR 0028](../../adr/0028-produktions-compose.md)) trennt
zwei Netze:

| Netz | Dienste | Zweck |
|---|---|---|
| `edge` | `caddy`, `server` | veröffentlichte Ports und ausgehende Verbindungen (ACME, SMTP, Kanäle, OIDC) |
| `internal` | `server`, `postgres`, `loki`, `migrate`, `backup`, `restore` | ohne Route nach außen (`internal: true`) |

- **80/443** gehen an Caddy (TLS per ACME, Reverse-Proxy zu `server:8080`, Admin-API aus). Nur Caddys feste Adresse
  (`SERMON_PROXY_IP`, ab Werk `172.30.78.10`) steht in `SERMON_TRUSTED_PROXIES`.
- **8443** (Agent-API, mTLS 1.3) veröffentlicht Docker direkt am Server, nicht über Caddy: So sieht der Server die
  echten Adressen der Agents, die er für die Erkennung kopierter Schlüssel braucht (T4).
- PostgreSQL und Loki haben keinen veröffentlichten Port. Loki hat keine eigene Anmeldung – nur der Server erreicht es
  und nennt bei jeder Anfrage die Organisation (T35).
- Jeder Container ist `read_only`, ohne Capabilities (Ausnahmen: Caddy bindet 80/443, PostgreSQL wechselt den User)
  und ohne neue Privilegien; kein Container bekommt den Docker-Socket (T73, `just lint-compose`).
- **Firewall:** Docker veröffentlicht Ports an Host-Firewalls wie `ufw` vorbei. Wollen Sie den Agent-Port auf die Netze
  Ihrer Hosts beschränken, tun Sie das in der Kette `DOCKER-USER` oder in der Firewall des Providers.

### Variablen, die die Sicherheit betreffen

Alle Variablen mit Voreinstellung stehen in der [Referenz](configuration.md). Für die Sicherheit entscheidend:

- `SERMON_MODE` nie auf `development` – dort gehen Einladungen ohne Mail und `http://`.
- `SERMON_ALLOWED_ORIGINS` genau die Adresse der UI (`https://…`); sie schützt vor CSRF.
- `SERMON_TRUSTED_PROXIES` nur der eigene Reverse-Proxy. Zu weit gefasst, wählt jeder Client seine Adresse für
  Rate-Limits und Audit-Log selbst.
- `SERMON_OUTBOUND_ALLOWLIST` leer lassen oder nur die internen Netze, die Kanäle wirklich brauchen (etwa ein interner
  Mattermost oder der OIDC-Provider). Alles andere ist SSRF-Fläche (T43).
- `SERMON_MASTER_KEYS_FILE` öffnet alle versiegelten Werte (CA-Schlüssel, TOTP-Seeds, Zugangsdaten; A3). Die Datei
  liegt `0600` im Volume `sermon_server-state`; sichern Sie sie getrennt von den Backups.
- `SERMON_METRICS_ADDR` ist ab Werk aus. Der Endpunkt hat keine Anmeldung, nur Werte der Instanz – binden Sie ihn
  nur an Loopback oder ein internes Netz ([Betrieb](operations.md#self-monitoring)).
- `SERMON_AGENT_HOSTNAMES` nur die Namen, unter denen Agents den Server erreichen.

### Secrets und Dateirechte

| Was | Wo | Rechte und Hinweise |
|---|---|---|
| Datenbank-Passwörter und -URLs | `deploy/compose/secrets/` (`secrets.sh`) | Verzeichnis `0700`, Dateien `0644` (die Container lesen sie als eigene User über Bind-Mounts – geschützt ist das Verzeichnis). `secrets.sh` überschreibt nie. `postgres_password` (Superuser) braucht nur der Datenbank-Container, `database_migrate_url` nur `migrate` und `restore`, `database_backup_url` nur `backup`. |
| SMTP-Passwort, OIDC-Client-Secret | `secrets/smtp_password`, `secrets/oidc_client_secret` | ohne Zeilenumbruch |
| Master-Keys | `/var/lib/sermon/master_keys` im Volume `sermon_server-state` | `0600`; nie ins Git, getrennt sichern |
| Öffentliche Backup-Schlüssel | `secrets/backup_recipients` | nicht geheim |
| Geheimer Backup-Schlüssel, gesicherte Master-Keys | `secrets/backup_identity`, `secrets/master_keys` | **nur für einen Restore** anlegen und danach löschen ([Backup](../backup.md)) |
| Einstellungen | `deploy/compose/.env` | keine Geheimnisse |

Wer root auf dem Server-Host ist, hat alles. Halten Sie den Host klein (nur Docker und SerMon), mit Updates und
SSH nur mit Schlüsseln – und überwachen Sie ihn selbst mit einem Agent.

### Anmeldung und Konten

- **Superadmins** verwalten die Instanz, sehen aber keine Daten der Organisationen. Geben Sie ihnen einen zweiten
  Faktor, am besten einen Passkey, und richten Sie mindestens zwei ein, damit ein verlorener Faktor die Instanz nicht
  aussperrt. Recovery-Codes offline aufbewahren.
- **Organisationen** können die Zwei-Faktor-Anmeldung verpflichtend machen (Seite „Organisation“). Empfohlen für jede
  Organisation mit Operatoren oder Admins: Sie starten Updates und Neustarts.
- **Passkeys** brauchen einen DNS-Namen mit Punkt; unter einer IP-Adresse läuft SerMon ohne sie (`passkeys are off` im
  Log).
- **OIDC:** `SERMON_OIDC_MFA=trusted` nur, wenn der Provider selbst einen zweiten Faktor erzwingt. Mit JIT
  (`SERMON_OIDC_JIT`) kann der Admin einer Organisation jedes Mitglied einer zugeordneten Provider-Gruppe aufnehmen;
  schalten Sie es ab, wenn das nicht gewollt ist. Liegt der Provider intern, braucht er einen Eintrag in
  `SERMON_OUTBOUND_ALLOWLIST`.
- Sitzungen enden nach 30 Minuten ohne Aktivität, spätestens nach 12 Stunden. Ab dem fünften Fehlversuch in Folge ist
  ein Konto 1, 2, 4 … bis 15 Minuten gesperrt. Sensible Aktionen verlangen eine erneute Bestätigung; sie gilt 15 Minuten
  ([Erneute Bestätigung](../../help/de/concepts/step-up.md)).

### Zugangsdaten der Module

Zugangsdaten speichert der Server versiegelt; zum Agent gehen sie nur über den mTLS-Stream und bleiben dort nur im
Speicher (T109). Trotzdem gilt: Jeder Node der Gruppe bekommt sie. Deshalb:

- **Eigene Monitoring-User mit wenigen Rechten:** PostgreSQL mit `pg_monitor`
  ([Modul](../../help/de/modules/postgres.md)), MySQL mit `PROCESS` und `REPLICATION CLIENT` bzw. MariaDB mit
  `REPLICA MONITOR` ([Modul](../../help/de/modules/mysql.md)), Redis mit einem ACL-User, der nur `INFO` darf
  ([Modul](../../help/de/modules/redis.md)), für Prometheus ein Token, das nur `/metrics` liest
  ([Modul](../../help/de/modules/prometheus.md)).
- **Sockets statt Ports**, wo es geht: Den Pfad legt nur der Dienst an, und PostgreSQL braucht mit `peer` gar kein
  Passwort. Über TCP außerhalb von Loopback nur mit geprüftem TLS (`verify`).
- Hosts mit besonderen Zugangsdaten in **eigene Gruppen**: Zugangsdaten gelten je Gruppe.
- Den Docker-Socket nur, wo nötig ([Modul](../../help/de/modules/docker_socket.md)); das Modul `docker` liest die
  cgroups ohne Socket.

## Was ein übernommener Server kann – und was nicht

Die zentrale Annahme: Wer den Server oder ein Admin-Konto übernimmt, soll nicht root auf allen Nodes werden (A1).

**Er kann** (mit den Voreinstellungen, soweit die Vetos es erlauben):

- Updates aus den signierten Quellen der Distribution installieren lassen (`allow_remote_updates`) und den Agent auf
  eine neuere, signierte Version bringen (`allow_self_update`, T40);
- Logs unter `log_file_prefixes` und aus dem Journal anfordern, die Live-Ansicht starten (beides redigiert, T42, T81);
- Module gegen die erlaubten Ziele laufen lassen und dafür Zugangsdaten senden (T107, T109);
- Schlüssel rotieren lassen, Nodes sperren oder deaktivieren, Alerts und Benachrichtigungen umleiten (T67, T69);
- alle Daten der Instanz lesen, auch die Zugangsdaten der Kanäle und Module: Er hält die Master-Keys und die Rolle
  `sermon_app`. RLS schützt vor Fehlern der Anwendung, nicht vor einem übernommenen Prozess (T32, T33);
- bei der nächsten Installation per `curl | sh` ein anderes Skript ausliefern (T50).

**Er kann nicht:**

- beliebige Befehle oder Skripte auf einem Host ausführen – das Protokoll kennt nur typisierte Befehle, der Helper nur
  feste Operationen ohne freie Argumente (T12, T38, T40);
- einen fremden, unsignierten oder älteren Agent installieren lassen (T40, T48);
- einen Host neu starten, solange `allow_remote_reboot = false` gilt (T90);
- `agent.toml` oder `helper.toml` ändern, die Schlüssel des Agents oder `/etc/sermon` lesen (T81, T108);
- Module in andere Netze, zu anderen Namen oder Ports schicken als erlaubt, den Docker-Socket ohne
  `allow_docker_socket` benutzen (T107, T108);
- das Schema ändern oder Migrationen einspielen – die Owner-Rolle kennt nur der Service `migrate` (T73);
- gesicherte Backups entschlüsseln: Der geheime Backup-Schlüssel liegt nicht auf dem Server (T116).

Für Hosts, auf denen auch das zu viel ist: `install.sh --no-helper` (keine Fernsteuerung), `allow_logs = false`,
`allow_live_view = false`, `allow_modules = false`, und Updates des Agents über Pakete statt Self-Update.
