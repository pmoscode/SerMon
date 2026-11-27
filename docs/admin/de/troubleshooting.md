# Fehlersuche

Typische Probleme mit den Befehlen, die sie eingrenzen. Befehle für den Server laufen in `deploy/compose/`, die für
einen Host als root auf dem Host. Unter OpenRC statt `systemctl`/`journalctl` die Befehle im Abschnitt
[OpenRC](#openrc).

## Erste Blicke

```sh
# Server
docker compose ps                                   # laufen alle Dienste? migrate: "exited (0)"
docker compose logs --since 1h server | grep -v '"level":"INFO"'
curl -fsS https://sermon.example.org/readyz          # {"status":"ready","checks":{"database":"ok","loki":"ok"}}

# Host
systemctl status sermon-agent
journalctl -u sermon-agent --since -1h
sudo -u sermon sermon-agent helper status            # Root-Helper erreichbar? was erlaubt er?
```

Superadmins sehen Probleme der Instanz zusätzlich als Band in der UI und per Mail
([Self-Monitoring](operations.md#self-monitoring)).

## Der Agent verbindet sich nicht

Das Log des Agents nennt den Grund in einer Zeile `session failed: …`:

| Meldung (Ausschnitt) | Ursache | Abhilfe |
|---|---|---|
| `the server rejected this agent's certificate (revoked, node locked or removed)` | Das Zertifikat steht auf der Denylist: Node deaktiviert, gelöscht, gesperrt oder Schlüssel widerrufen | Node-Seite, Reiter „Identität“: bei einer harmlosen Sperre entsperren; sonst neu anmelden (unten) |
| `the agent certificate has expired` | zu lange offline, die automatische Rotation kam nicht dazu | neu anmelden (unten) |
| `certificate not valid for name …` | Der Name in `agent_url` fehlt in `SERMON_AGENT_HOSTNAMES` | `SERMON_AGENT_HOSTNAMES` ergänzen, `docker compose up -d` |
| `UnknownIssuer` | Auf Port 8443 antwortet nicht die Agent-API dieses Servers: ein TLS-Proxy davor, oder eine neu aufgesetzte Instanz mit neuer Agent-CA | Port 8443 muss direkt beim Server ankommen; nach einem neuen `init` die Hosts neu anmelden |
| `certificate not valid yet`, `certificate expired` | Uhr des Hosts falsch – oder die Agent-CA ist abgelaufen | Zeit synchronisieren (unten); Ablauf auf der Seite „Agent-CA“ prüfen ([Betrieb](operations.md#zertifikate-und-schlüssel)) |
| `connection refused`, Timeout | Firewall, falscher Port | Port 8443 vom Host aus prüfen (unten) |

**Erreichbarkeit und Zertifikat der Agent-API** (es ist von der Agent-CA, nicht öffentlich – das ist richtig so):

```sh
openssl s_client -connect sermon.example.org:8443 -servername sermon.example.org </dev/null 2>/dev/null \
  | openssl x509 -noout -subject -ext subjectAltName -dates
```

Der Name aus `agent_url` in `/etc/sermon/agent.toml` (ab Werk der Host von `url` mit Port 8443) muss unter
`subjectAltName` stehen.

**Uhr:** `timedatectl` bzw. `chronyc tracking`. Zertifikate gelten ab 5 Minuten vor ihrer Ausstellung; Werte, die mehr
als 5 Minuten in der Zukunft oder älter als 24 Stunden sind, verwirft der Server (`sermon_server_ingest_rejected_samples_total`).
Die Metriken `sermon_time_synchronized` und `sermon_time_offset_seconds` zeigen die Uhr jedes Nodes.

**Neu anmelden:** In der UI ein Token für die Gruppe des Nodes erzeugen; war der Host schon angemeldet (gleiche
`machine-id`), vorher auf der Node-Seite „Übernahme erlauben“ (eine Stunde). Dann den Installationsbefehl aus der UI
mit `--force` ausführen, oder direkt:

```sh
 export SERMON_ENROLLMENT_TOKEN=sme_…
sudo --preserve-env=SERMON_ENROLLMENT_TOKEN -u sermon sermon-agent enroll --force
systemctl restart sermon-agent
```

### Das Enrollment scheitert

| Meldung | Abhilfe |
|---|---|
| `the enrollment token is invalid, expired, revoked or used up` | Neues Token erzeugen; der Server sagt absichtlich nicht, welcher Fall vorliegt. |
| `this host (same machine-id) is already registered in the organization` | „Übernahme erlauben“ am vorhandenen Node, dann mit einem Token **seiner** Gruppe. Geklonte VMs brauchen eine eigene `machine-id`. |
| `too many enrollment attempts` | eine Minute warten (Rate-Limit je Adresse). |
| `no server certificate matches the pinned fingerprint …` | Fingerprint neu bestimmen (unten). |
| `agent.toml points to …, not …` (von `install.sh`) | Die Konfiguration zeigt auf einen anderen Server: mit `--force` ersetzen, wenn das gewollt ist. |
| `signature check failed` (von `install.sh`) | Binary und Signatur in `downloads/` passen nicht zum Release-Schlüssel oder zur Version des Servers. Nichts installieren, Herkunft prüfen. |

Fingerprint eines selbstsignierten Zertifikats für `--ca-fingerprint`:

```sh
openssl s_client -connect sermon.example.org:443 -servername sermon.example.org </dev/null 2>/dev/null \
  | openssl x509 -noout -fingerprint -sha256
```

Aus `sha256 Fingerprint=AB:CD:…` wird `--ca-fingerprint sha256:AB:CD:…`.

## Der Node ist verbunden, aber es fehlen Metriken

- Lokal sammeln, ohne zu senden: `sudo -u sermon sermon-agent collect --seconds 5`.
- In `/metrics` ([Betrieb](operations.md#self-monitoring)): `sermon_server_ingest_rejected_samples_total` (Zeit
  unplausibel oder Rate-Limit), `…_dropped_series_total` (Serien-Limit), `…_unknown_samples_total` (Metrik nicht im
  Katalog, meist ein Agent neuer als der Server), `…_late_samples_total`.
- Kontingente der Organisation (Samples pro Sekunde und Node, Serien pro Node) auf der Seite „Organisationen“. Drosselt
  der Server, schreibt der Agent das ins Log.
- Ingest-Lag hoch: Last von Server und Datenbank prüfen (`docker stats`).

## Logs fehlen

- **Auf dem Host verboten:** `allow_logs = false` in `agent.toml` – die Log-Einstellungen zeigen dann
  „allow_logs = false“ beim Node. Logdateien außerhalb von `log_file_prefixes` werden nie gelesen.
- **In der Organisation aus:** „Organisation → Log-Einstellungen“, Schalter „Logs senden“ je Gruppe, Mindest-Schweregrad,
  ausgeschlossene Units ([Log-Einstellungen](../../help/de/concepts/log_settings.md)). Lehnt ein Agent die Einstellungen
  ab (etwa eine Regex), steht der Grund unter „Stand pro Node“.
- **Loki:** Ohne `SERMON_LOKI_URL` warnt der Server beim Start (`SERMON_LOKI_URL is not set`). Ist Loki nicht erreichbar,
  meldet das Self-Monitoring „The server drops log entries“; dann `docker compose logs loki` und `/readyz` prüfen.
- **Security-Events fehlen** (kein journald, z. B. Alpine): Die Node-Seite zeigt die Quelle; siehe
  [Quelle der Security-Events](../../help/de/concepts/security-source.md) und `install.sh --grant-log-access`.
- Logs sind älter als die Aufbewahrung der Organisation: Loki hat sie gelöscht.

## Module laufen nicht

Die Node-Seite zeigt im Reiter „Module“ den Zustand und den Grund. Der Agent prüft die Konfiguration selbst; der Grund
nennt nur, was der Server geschickt hat.

| Zustand | Bedeutung | Prüfen |
|---|---|---|
| Vom Host verboten (`vetoed`) | `agent.toml` verbietet es: `allow_modules = false`, oder ein Ziel liegt außerhalb von `module_targets`, `module_hosts`, `module_ports`, `module_sockets`, `module_file_prefixes`; ein Socket einer Container-Runtime ohne `allow_docker_socket` | `[local]` anpassen ([Härtung](hardening.md#lokale-vetos-in-agenttoml)), Agent neu starten. Symlinks müssen auch mit ihrem Ziel in einem Präfix liegen. |
| Konfiguration abgelehnt (`invalid_config`) | Die Einstellung passt nicht zum Schema des Moduls im Agent | Grund lesen; ist der Agent älter als der Server, kennt er neue Felder nicht – Agent aktualisieren. |
| Fehler (`error`), Zeitüberschreitung (`timeout`) | Der Dienst antwortet nicht wie erwartet: falsche Zugangsdaten, fehlende Rechte, Dienst aus, zu langsam | Grund lesen; fehlt ein Recht, nennt das Modul das nötige `GRANT`. Den Dienst vom Host aus prüfen. |
| Hängt (`hung`) | Ein Lauf kehrt nicht zurück (auch eine Prüfung beim Start über 30 s); ein neuer startet erst danach | Namensauflösung und Socket des Dienstes prüfen; hält es an, `journalctl -u sermon-agent` lesen und den Agent neu starten. |
| Abgestürzt (`crashed`) | Ein Fehler im Modul; es startet nach einer Pause neu, Agent und andere Module laufen weiter | Die Meldung aus dem Log des Agents als Fehler melden. |

## Root-Helper und Updates

```sh
sudo -u sermon sermon-agent helper status
systemctl status sermon-helper.socket
journalctl -u sermon-helper --since -1d             # jede Anfrage, auch abgelehnte
journalctl -u 'sermon-helper-run-*' --since -1d     # die Läufe (Paketmanager, Neustart, Self-Update)
```

| Symptom | Prüfen |
|---|---|
| `helper status` meldet, dass `sermon-helper` nicht erreichbar ist | Helper nicht installiert oder abgeschaltet (`--no-helper`, `systemctl disable --now sermon-helper.socket`): `install.sh` erneut ohne `--no-helper` ausführen. |
| Jede Anfrage abgelehnt, im Journal `unsafe configuration` | `/etc/sermon/helper.toml` oder `/etc/sermon` gehört nicht root oder ist für Gruppe/andere schreibbar. Datei: `chown root:root`, `chmod 0644`; Verzeichnis: `chown root:sermon`, `chmod 0750`. |
| Knöpfe „Jetzt aktualisieren“, „Neu starten“ gesperrt | Der Host verbietet es (`allow_remote_updates`, `allow_remote_reboot`); die UI nennt den Grund. |
| Rollout überspringt Nodes | offline, ohne Helper, vom Host verboten, Fensterende; im Rollout steht der Grund. |
| Nichts startet mehr | Updates der Organisation pausiert (Hinweis oben auf der Seite „Updates“), Fenster der Policy noch nicht offen, oder `maxParallel` erreicht. |
| Agent-Update nicht angeboten | Installation per Paket (dann der Paketmanager), keine neuere signierte Version im Kanal (`downloads/` bzw. `downloads/beta/`), `allow_self_update = false`. |
| Agent-Update „zurückgerollt“ | Die neue Version hat sich nicht binnen 2 Minuten verbunden: Log des Agents nach dem Neustart lesen. |

## Alerts kommen nicht an

1. **Gibt es den Alert?** Seite „Alerts“. Wenn nicht: Regel abgeschaltet, Schwelle nicht erreicht, oder für
   „Offline“/„Reboot“ ein Wartungsfenster.
2. **Stumm?** Eine Silence oder ein Wartungsfenster unterdrückt nur die Benachrichtigung, der Alert bleibt sichtbar.
3. **Wohin?** Sobald es eine einzige Policy gibt (auch eine abgeschaltete), gehen Alerts nur noch über Policies.
4. **Zustellung:** „Benachrichtigungen → Protokoll“ zeigt jeden Versuch mit Fehler (nur der Host des Ziels, nie
   Zugangsdaten). **Test senden** am Kanal stellt sofort zu.
5. **Gesperrt durch den SSRF-Schutz** (`target address not allowed`, `plain http only to allowlisted targets`,
   `redirect to another host`, Zähler `sermon_server_notify_blocked_total`): Liegt das Ziel im internen Netz, dessen
   Netz in `SERMON_OUTBOUND_ALLOWLIST` eintragen und `docker compose up -d` ausführen. Öffentliche Ziele nur über
   `https://`; Weiterleitungen nur auf denselben Host.
6. **E-Mail-Kanäle** nutzen das SMTP der Instanz; je Organisation höchstens 100 Mails pro Stunde
   (`sermon_server_notify_mails_deferred_total`). Eine gesperrte Organisation benachrichtigt nicht.
7. **Mails an Superadmins** (Self-Monitoring) und Einladungen: SMTP-Variablen prüfen; Fehler stehen im Log des Servers.

## Erneute Bestätigung (Step-up) klappt nicht

- Die Bestätigung gilt 15 Minuten; danach fragt SerMon vor der nächsten sensiblen Aktion neu
  ([Erneute Bestätigung](../../help/de/concepts/step-up.md)).
- Fünf falsche Codes oder Passwörter sperren das Konto zunehmend länger (bis 15 Minuten). Uhr des Telefons prüfen: TOTP
  erlaubt nur ±1 Schritt (30 s).
- **Über OIDC:** SerMon verlangt eine frische Anmeldung beim Provider (`max_age=0`). Im Log des Servers steht dann
  `oidc flow failed` mit dem Grund: `no fresh login at the provider (auth_time)` – der Provider meldet keine neue
  Anmeldung, er muss `max_age` beachten; `provider login without a second factor for a user with one` – der User hat in
  SerMon einen zweiten Faktor, die Anmeldung beim Provider nannte keinen (`SERMON_OIDC_MFA`).
- Passkeys fehlen ganz: Die UI läuft unter einer IP-Adresse oder einem Namen ohne Punkt (`passkeys are off` im Log).

## Migrationen

- Der Server startet nicht, `migrate` endet mit Fehler: `docker compose logs migrate`. Der Server wartet auf ein
  erfolgreiches `migrate`.
- **Entwicklungsdatenbanken:** `detected … missing (out-of-order) migrations` heißt, die Datenbank hat eine höhere
  Migration als eine, die neu dazukam – typisch nach einem Wechsel zwischen Branches. SerMon spielt Migrationen nur in
  Reihenfolge ein. Für `just dev`: `just dev-reset` (löscht die Entwicklungsdaten). In Produktion kommt das nicht vor,
  solange nur Releases eingespielt werden.

## OpenRC

Unter OpenRC (Alpine) sammelt kein Logger die Ausgabe des Agents. Zum Nachsehen den Dienst anhalten und den Agent im
Vordergrund starten:

```sh
rc-service sermon-agent status
rc-service sermon-agent stop
su -s /bin/sh -c '/usr/local/bin/sermon-agent run --config /etc/sermon/agent.toml' sermon   # Paket: /usr/bin/
rc-service sermon-agent start
```

Der Helper schreibt über `logger` ins Syslog (`grep sermon-helper /var/log/messages`).
