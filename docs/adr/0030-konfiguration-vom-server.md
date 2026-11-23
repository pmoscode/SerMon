# 0030 – Konfiguration vom Server an die Agents

- Status: accepted
- Datum: 2026-11-23
- Bezug: KONZEPT.md §4.1, §5, §10; ADR 0013, ADR 0016, ADR 0025; IMPLEMENTIERUNGSPLAN M3-03, M3-04, M3-05; Threat
  Model T12, T42, T81

## Kontext

KONZEPT §4.1 sieht vor, dass fast nichts lokal konfiguriert wird. Alles Weitere kommt versioniert vom Server. Mit den
Logs (E3.1) braucht der Agent erstmals solche Einstellungen:

- ob Logs gesendet werden, ab welcher Severity, ohne welche Units
- welche Dateien per Glob gelesen werden (M3-05)
- die Redaction-Regeln der Org (M3-02)

Das Hello trägt seit M0-05 ein `config_version`, das der Server bisher nicht liest. Der Server ist die Stelle, die ein
Angreifer am ehesten übernimmt, um viele Nodes zu erreichen (T40). Die Konfiguration ist deshalb ein Weg, den es zu
begrenzen gilt.

## Optionen

1. **Konfiguration im Hello-Ack**: nur beim Verbindungsaufbau. Änderungen kämen erst nach einem Reconnect an.
2. **Eigene Nachricht im Stream** (`ConfigUpdate`), beim Aufbau und bei Änderungen, mit Rückmeldung des Agents
   (`ConfigApplied`).
3. **Abruf durch den Agent** über eine eigene RPC. Das wäre ein zweiter Kanal neben dem Stream und hätte keinen
   Vorteil.

## Entscheidung

Gewählt: **Option 2.**

- **Was der Server steuern darf:**
  - nur die Auswahl der Logs: an oder aus, Mindest-Severity, ausgeschlossene Units und Datei-Globs
  - zusätzliche Redaction-Regeln

  Er kann die eingebauten Redaction-Regeln nicht abschalten. Er kann keine Pfade außerhalb der lokal erlaubten
  Präfixe lesen lassen (M3-05) und keine Befehle, Intervalle der Security-Quellen oder Rechte ändern. Die Nachricht
  ist typisiert, freie Felder gibt es nicht (T12).
- **Lokales Veto gewinnt:** Mit `allow_logs = false` in `agent.toml` sendet der Agent keine Logs, egal was der Server
  schickt. Er meldet das Veto zurück, und die UI zeigt es (M3-12).
- **Version:** Ein Zähler pro Org (`organizations.agent_config_version`) beginnt bei 1. Jede Änderung an
  Log-Einstellungen oder Redaction-Regeln erhöht ihn in derselben Transaktion. Die Konfiguration eines Nodes besteht
  aus den Einstellungen seiner Gruppe und den Regeln der Org. Ohne gespeicherte Einstellungen gilt der Standard: Logs
  an, ohne Filter (KONZEPT §10.1).
- **Verteilung:**
  - Der Server sendet `ConfigUpdate`, wenn seine Version von der im Hello abweicht. Er prüft auf Abweichung, nicht nur
    auf „größer“, damit auch ein Restore der Datenbank ankommt.
  - Danach sendet er bei jeder Änderung an die Sessions der Org auf dieser Instanz.
  - Bei der Nachprüfung jeder Minute (T56) prüft er die Version erneut. So kommen auch Änderungen über andere
    Instanzen an.
  - Eine Version geht pro Session höchstens einmal raus. Eine abgelehnte Konfiguration wird also nicht jede Minute
    erneut geschickt.
- **Agent:**
  - Er prüft die Konfiguration vollständig, bevor er sie übernimmt (Regexes kompilieren, Grenzen).
  - Er speichert sie atomar (0600) im State-Verzeichnis.
  - Er meldet `ConfigApplied{version, error, logs_vetoed}`.
  - Bei einem Fehler behält er die letzte gültige Konfiguration, und das Hello nennt weiter deren Version.
  - Logs sendet er erst, wenn eine Konfiguration sie erlaubt. Ein Server ohne diese Nachricht bekommt deshalb nie
    Logs.
- **Prüfung im Server:** Grenzen wie im Schema; Regexes mit Go-`regexp` (RE2), damit ein Admin Fehler sofort sieht.
  Was nur der Rust-`regex` ablehnt, meldet der Agent; der Server speichert die Meldung am Node (M3-12 zeigt sie).
- **Recht:** neue Action `manage_log_settings` (Admin der Org); Ändern nur mit Step-up und Audit-Eintrag. Lesen dürfen
  ebenfalls nur Admins, denn die Regeln können interne Kennungen nennen.

## Konsequenzen

- Positiv:
  - Änderungen erreichen verbundene Agents sofort, andere Instanzen nach höchstens einer Minute.
  - Der Agent arbeitet mit der letzten Konfiguration auch ohne Server weiter.
  - Was der Server steuern kann, ist eng und typisiert.
- Negativ: Eine Änderung erhöht die Version der ganzen Org. Agents anderer Gruppen bekommen denselben Inhalt mit neuer
  Version; das ist harmlos und billig.
- Security:
  - Ein kompromittierter Server kann Logs anschalten und Units auswählen, aber nur innerhalb des lokalen Vetos und der
    lokal erlaubten Pfade (T81).
  - Redaction-Regeln des Servers können nur zusätzlich maskieren.
  - Logs bleiben Daten des Hosts und werden nur angezeigt.
