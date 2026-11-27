# Betrieb

Was im laufenden Betrieb zu tun und zu wissen ist: Self-Monitoring, Aufbewahrung, Löschen, Enrollment, Updates,
Zertifikate und Kapazität. Installation und Aktualisieren des Servers: [Installation](../installation.md); Sichern:
[Backup und Restore](../backup.md); alle Variablen: [Referenz](configuration.md).

## Self-Monitoring

Der Server prüft sich jede Minute selbst ([ADR 0027](../../adr/0027-server-ereignisse-und-self-monitoring.md)):

| Prüfung | Stufen |
|---|---|
| Ingest-Lag: wie alt der neueste Batch eines Agents beim Eintreffen ist | Warnung ab 60 s, kritisch ab 10 min |
| Verworfene Samples (Serien-Limit, unplausible Zeiten, zu späte Fenster, unbekannte Metriken) | Warnung, wenn innerhalb von 15 min welche dazukommen |
| Verworfene seltene Security-Events (Anmeldungen, sudo, Konten, System) durch das Rate-Limit eines Nodes | Warnung |
| Größe der Datenbank gegen `SERMON_DATABASE_SIZE_LIMIT` | Warnung ab 80 %, kritisch ab 90 % |
| Loki nicht erreichbar: der Ingest verwirft Logs (Metriken, Events und Alerts laufen weiter) | Warnung |
| Ablauf der Agent-CA: wann die erste vertraute CA abläuft | Warnung ab 90 Tagen vor dem Ablauf, kritisch ab 30 Tagen |

- Bei jedem Wechsel der Stufe – auch bei der Entwarnung – bekommen alle Superadmins eine Mail (auf Englisch) über das
  SMTP der Instanz. In der UI sehen Superadmins offene Probleme als Band unter der Kopfzeile. Organisationen sehen
  nichts davon.
- Ohne `SERMON_DATABASE_SIZE_LIMIT` prüft der Server die Größe nicht. Er kennt nur die Größe der Datenbank, nicht den
  freien Platz: Setzen Sie die Grenze unter den Platz des Datenträgers und **installieren Sie einen Agent auf dem
  Server-Host** – seine Alerts zu Disk, Speicher und CPU decken die Volumes mit ab.
- **Health:** `curl -fsS https://sermon.example.org/readyz` antwortet mit dem Zustand von Datenbank und Loki
  (`{"status":"ready","checks":{…}}`, sonst HTTP 503); `/healthz` sagt nur, dass der Prozess läuft.
- **`/metrics`** (Prometheus-Format, ohne Anmeldung, nur Werte der Instanz) ist ab Werk aus. In der Compose z. B. mit
  einer Datei `deploy/compose/compose.override.yaml`, nur auf Loopback des Hosts:

  ```yaml
  services:
    server:
      environment:
        SERMON_METRICS_ADDR: ":9464"
      ports: ["127.0.0.1:9464:9464"]
  ```

  Danach `curl -s http://127.0.0.1:9464/metrics`. Der Server warnt beim Start, weil die Adresse im Container nicht
  Loopback ist – erreichbar ist sie nur über den Port auf `127.0.0.1` des Hosts. Wichtige Zähler:
  `sermon_server_ingest_lag_seconds`, `sermon_server_agent_sessions`, `sermon_server_ingest_rejected_samples_total`,
  `sermon_server_ingest_dropped_series_total`, `sermon_server_ingest_dropped_log_entries_total`,
  `sermon_server_notify_failed_total`, `sermon_server_notify_blocked_total`.

## Aufbewahrung

| Daten | Aufbewahrung | Einstellbar |
|---|---|---|
| Metriken, 1 s | 15 Minuten im Speicher des Servers | nein |
| Metriken, 10 s / 1 min / 1 h | 24 Stunden / 30 Tage / 1 Jahr | nein |
| Logs (Loki) | ab Werk 14 Tage | je Organisation |
| Security-Events, Verlauf der Alerts | ab Werk 1 Jahr | je Organisation |
| Audit-Logs | `SERMON_AUDIT_RETENTION`, ab Werk 2 Jahre | Betreiber |

- **Je Organisation** stellen Admins unter „Organisation“ die Aufbewahrung ein: Logs 1 bis 14 Tage, Security-Events
  und Verlauf der Alerts 7 bis 365 Tage ([Aufbewahrung](../../help/de/concepts/retention.md)). Als Superadmin begrenzen
  Sie das je Organisation mit dem Kontingent „Obergrenze der Retention“; ein gesenktes Kontingent löscht nichts.
- **Loki** behält Logs höchstens `retention_period: 336h` (`deploy/compose/loki.yaml`); das ist die Grenze der Instanz.
  Heben Sie sie nicht an: Die Einstellungen der Organisationen gehen nicht darüber hinaus. Loki löscht etwa einmal am
  Tag, Logs sind also bis zu einem Tag länger sichtbar.
- Audit-Logs verlieren alte Einträge vom Anfang der Kette her; ein Eintrag `audit.prune` hält die Kette prüfbar.
- Die vollständige Liste mit allen Fristen (Sitzungen, Einladungen, Zustellprotokoll, Anomalien …) steht in der
  [Vorlage zur DSGVO-Dokumentation](../../privacy/de/README.md).

## Löschen: Nodes, Organisationen, Konten

Alle Löschjobs laufen stündlich; die Fristen stehen in der [Referenz](configuration.md#fristen).

- **Node löschen** (Admins der Gruppe, mit Hostname und erneuter Bestätigung): Der Node wird sofort deaktiviert, seine
  Zertifikate widerrufen. Nach `SERMON_NODE_DELETION_GRACE` (ab Werk 7 Tage) löscht der Server seine Logs, Metriken,
  Anomalien, Events, Alerts und Login-Quellen; bis dahin lässt sich das Löschen abbrechen
  ([ADR 0041](../../adr/0041-nodes-deaktivieren-loeschen.md)). Auf dem Host danach `sermon-agent uninstall --yes` bzw.
  das Paket entfernen.
- **Organisation löschen** (Superadmin, Seite „Organisationen“): „Zum Löschen vormerken“ sperrt sie sofort – Mitglieder
  abgemeldet, Agents getrennt, Tokens und Einladungen ungültig – und löscht sie nach `SERMON_ORG_DELETION_GRACE` (ab
  Werk 30 Tage) mit allen Daten und den Konten ohne weitere Mitgliedschaft. Die Admins bekommen Mails beim Sperren,
  beim Aufheben und 7 Tage vor dem Löschen. Während der Sperre lässt sich ihr Audit-Log mit Hash-Kette herunterladen
  ([ADR 0029](../../adr/0029-organisationen-verwalten-sperren-loeschen.md)).
- **Konten:** Ein Konto ohne Mitgliedschaft löscht der Server nach `SERMON_ACCOUNT_DELETION_GRACE` (ab Werk 30 Tage;
  Superadmins nie). Auf Antrag löscht es ein Superadmin sofort (Seite „Organisationen“).

## Enrollment-Tokens

Operatoren erzeugen Tokens unter „Nodes → Node hinzufügen“ für eine Gruppe: gültig 5 Minuten bis 7 Tage, ab Werk 1
Stunde, 1 bis 1000 Nutzungen, ab Werk 1. Der Server speichert nur den Hash; ein Token lässt sich widerrufen.

- Für einen Rollout auf viele Hosts ein Token mit passender Zahl an Nutzungen und kurzer Gültigkeit nehmen, nicht eines
  für Wochen.
- Jede Nutzung löst die Regel `security.enrollment.token_used` aus (Warnung, T3) – so fällt ein gestohlenes Token auf.
  Für einen geplanten Massen-Rollout vorher eine Silence anlegen, statt die Regel abzuschalten.
- Wird ein Host neu installiert, kennt der Server seine `machine-id` und lehnt ab; „Übernahme erlauben“ auf der
  Node-Seite (Reiter „Identität“) öffnet dafür eine Stunde ([Identität](../../help/de/concepts/node-identity.md)).
- Ein Token ins Skript nur über `SERMON_ENROLLMENT_TOKEN` oder `--token-file` geben, nie als Argument
  ([Härtung](hardening.md#optionen-von-installsh)).

## Updates von Agents und Hosts

**Reihenfolge bei einer neuen Version:** erst den Server aktualisieren ([Installation](../installation.md)), dann die
Agents. Agents werden nie neuer als ihr Server (außer im Kanal `beta`).

- **Kanäle:** `stable` ist die Version des Servers – die signierten Binaries direkt in `SERMON_AGENT_DOWNLOADS_DIR`
  (Compose: im Image des Releases, `/srv/downloads`); `beta` sind dieselben Dateinamen in `/srv/downloads/beta/`
  (eingehängt per `compose.override.yaml`, [Installation](../installation.md#kanal-beta)). Der Server liest sie beim
  Start und alle 5 Minuten neu und prüft ihre Signaturen ([ADR 0036](../../adr/0036-self-update-und-paket-repos.md)).
- **Per `install.sh` installiert:** Operatoren starten „Agent aktualisieren“ auf der Node-Seite oder die Update-Policy
  der Gruppe tut es im Fenster (`agentUpdate`). Der Helper prüft die Signatur, ersetzt die Binary und legt die alte
  zurück, wenn die neue sich nicht binnen 2 Minuten verbindet. Er erneuert sich mit dem Agent. Voraussetzung:
  `allow_self_update = true`.
- **Per Paket installiert** (`--repo`, .deb, .rpm, .apk): der Paketmanager, auch über die Update-Policy.
- **Paket-Updates der Hosts:** von Hand oder per Update-Policy als Rollout mit `maxParallel`, Health-Check und „beim
  ersten Fehlschlag stoppen“ ([Update-Policy](../../help/de/concepts/update-policy.md)). Bei einem Verdacht auf ein
  übernommenes Konto pausieren Admins alle Updates der Organisation (Seite „Updates“). Jeder Lauf steht im Audit-Log
  (`node.command`, `node.run_ended`).

## Zertifikate und Schlüssel

- **Agent-Zertifikate gelten 30 Tage.** Nach zwei Dritteln erzeugt der Agent selbst einen neuen Schlüssel und holt ein
  neues Zertifikat über den Stream; das alte gilt noch 1 Stunde. Zu tun ist nichts. Läuft das Zertifikat ab, während der Node offline ist (je nach letzter Rotation nach 10 bis 30
  Tagen ohne Verbindung), muss er neu angemeldet werden.
- **Rotation auf Abruf** (Verdacht auf einen kopierten Schlüssel): auf der Node-Seite, Reiter „Identität“ – rotieren,
  nur widerrufen, entsperren ([Identität](../../help/de/concepts/node-identity.md)).
- **Das Server-Zertifikat der Agent-API gilt 7 Tage** und wird im Speicher automatisch erneuert, für die Namen aus
  `SERMON_AGENT_HOSTNAMES`.
- **Agent-CA:** Das Root-Zertifikat gilt 10 Jahre, die Intermediate-CA, die alle Zertifikate ausstellt, 2 Jahre ab
  ihrer Erzeugung (bei `init` bzw. beim letzten CA-Wechsel). Der Server erneuert sie nicht von selbst; nach dem Ablauf
  lehnen Agents und Server einander ab, und jeder Host muss neu angemeldet werden. Das Self-Monitoring warnt die
  Superadmins ab 90 Tagen vor dem Ablauf (Mail und Band in der UI, kritisch ab 30 Tagen). Der **CA-Wechsel** auf der
  Seite **Agent-CA** (Superadmins, unter „Organisationen“, [Hilfe](../../help/de/pages/agent_ca.md)) zeigt Ablauf und
  Stand und führt ihn mit erneuter Bestätigung durch:
  1. **Umstellung starten:** Eine neue CA stellt ab dann alle Zertifikate aus; die Agents bekommen beim nächsten
     Verbinden ein Trust-Bundle mit beiden Roots und holen bei ihrer nächsten Rotation ein Zertifikat der neuen CA.
     Bestehende Agents verbinden sich weiter, neue melden sich normal an.
  2. **Vorige CA stilllegen**, sobald sie keine gültigen Zertifikate mehr hat (spätestens 30 Tage nach dem Start).
     Erst dann kommt auch das Server-Zertifikat der Agent-API von der neuen CA.

  Starten Sie die Umstellung also spätestens 30 Tage vor dem Ablauf, besser bei der ersten Warnung. Beide Schritte
  stehen im Audit-Log der Instanz (`pki.rollover_started`, `pki.authority_retired`).
- **Master-Keys:** nie ersetzen, kürzen oder umsortieren. Das Format kennt mehrere Schlüssel (der neueste zuerst), einen
  geprüften Ablauf zum Wechsel mit neuem Versiegeln gibt es in v1.0 nicht.
- **Release- und Paketschlüssel** und ihr Wechsel: [Installation](../installation.md) und
  [ADR 0036](../../adr/0036-self-update-und-paket-repos.md).

## Kapazität

- **Richtwert für 50 Nodes:** 4 vCPU, 8 GB RAM, 200 GB NVMe-SSD (KONZEPT §22). Der Testbetrieb mit dem
  Release-Kandidaten (V1-18) bestätigt oder korrigiert ihn; das Ergebnis kommt hierher.
- **Lasttest vorab (V1-08,** [Lasttest](load-test.md)**):** 50 virtuelle Nodes mit rund 110 Serien und 2 Logzeilen pro
  Sekunde brauchten im Mittel ein Fünftel eines Kerns und höchstens 2,5 GiB Speicher, ohne Lag. Den Platz bestimmen
  bei voller Aufbewahrung die 1-Minuten-Werte (rund 30 GiB nach 30 Tagen) und die Security-Events: bei dauerhaftem
  Brute Force auf allen Nodes rund 140 GiB im Jahr, sonst wenige GiB. Messen Sie die eigene Instanz mit
  `scripts/load-report.sh`.
- Je Node höchstens 2 000 aktive Serien, je Organisation 250 000 (T9); darüber verwirft der Ingest neue Serien
  (Self-Monitoring „verworfene Samples“). Kontingente je Organisation (Nodes, Mitglieder, Samples pro Sekunde, Serien)
  setzen Superadmins auf der Seite „Organisationen“.
- Loki ist auf 1 GiB Speicher begrenzt (`SERMON_LOKI_MEMORY`, `SERMON_LOKI_GOMEMLIMIT`); das reicht für einige hundert
  Nodes. Größere Instanzen heben beide gemeinsam an ([Installation](../installation.md)).
- Der Agent selbst bleibt unter `MemoryMax=128M` und `CPUQuota=20%` seiner Unit.

## Sichern und Datenschutz

- [Backup und Restore](../backup.md): `sermon-server backup` (verschlüsselt, ohne Metriken), Restore, pgBackRest für
  Metriken, das Loki-Volume. Vor jedem Update ein Backup.
- [Vorlage zur DSGVO-Dokumentation](../../privacy/de/README.md): welche personenbezogenen Daten SerMon hält, Fristen,
  technische und organisatorische Maßnahmen. Wer Fristen ändert, passt die eigene Fassung an.
