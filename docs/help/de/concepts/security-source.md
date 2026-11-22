# Quelle der Security-Events

Der Agent liest SSH-Anmeldungen, sudo, Konto-Änderungen, OOM-Kills und Neustarts aus den Logs des Hosts. Ohne Quelle meldet er davon nichts – die Security-Alerts bleiben dann still, obwohl etwas passiert.

## Welche Quelle

- **journald** (die meisten Distributionen): Jede Zeile trägt den Absender; die Events sind verifiziert.
- **SerMon-Vorlage für rsyslog** (`/var/log/sermon/security.log`): Die Zeilen tragen die Benutzer-ID des Absenders; Events von root sind verifiziert.
- **Klassische Logdatei** (`/var/log/auth.log`, `/var/log/secure`, `/var/log/messages`): Die Events zählen, sind aber [nicht verifiziert](help:concepts/unverified-source).
- **Keine:** kein journald und keine Datei, die der Agent lesen darf.

Neustarts erkennt der Agent ohne journald an der Boot-ID des Kernels; sie kommen also auch ohne lesbare Logdatei. Erst ab dem Neustart des Agents nach der Einrichtung zeigt die Node-Seite die neue Quelle.

## Was tun

- **Alpine mit busybox syslogd:** `/var/log/messages` gehört der Gruppe `wheel`. Den Agent mit `install.sh --grant-log-access` installieren (oder das Skript erneut damit ausführen): syslogd schreibt dann mit der Gruppe `sermon`.
- **rsyslog:** die SerMon-Vorlage einrichten (siehe [nicht verifizierte Quelle](help:concepts/unverified-source)); dann sind die Events auch verifiziert.
- Danach den Agent neu starten (`rc-service sermon-agent restart` bzw. `systemctl restart sermon-agent`).
