# Module

Module überwachen Dienste eines Hosts – Webserver, Datenbanken, Zertifikate, Disks. Sie sind in den Agent eingebaut,
ab Werk aus und laufen erst, wenn ein Admin sie für eine Node-Gruppe einschaltet.

## Einschalten

Unter **Organisation → Module** steht je Gruppe, welche Module die Agents ihrer Nodes kennen. **Einrichten** öffnet
das Formular des Moduls; jede Änderung fragt noch einmal nach dem Passwort, steht im Audit-Log und erreicht die
verbundenen Agents sofort. Der Server prüft die Eingaben vor, maßgeblich ist der Agent: Lehnt er eine Konfiguration
ab, zeigt der Node „Konfiguration abgelehnt“ mit dem Grund.

## Was der Host erlaubt

Wohin ein Modul sich verbinden darf, bestimmt der Host in seiner `agent.toml` (`[local]`): ab Werk nur `localhost`
(`module_targets`), Unix-Sockets unter `module_sockets`, Dateien unter `module_file_prefixes`. Mit
`allow_modules = false` läuft kein Modul. Der Server kann diese Grenzen nicht ändern.

## Zugangsdaten

Felder für Passwörter und Tokens zeigen nie einen Wert, nur „gesetzt“ oder „nicht gesetzt“. Für Datenbanken einen
eigenen Monitoring-User mit wenigen Rechten anlegen.

## Vorschläge

Der Agent sieht nach, welche Dienste auf dem Host laufen (Prozesse, lauschende Ports, Units), und schlägt passende
Module vor – etwa „nginx erkannt – Modul aktivieren?“ am Node und in der Fleet. Eingeschaltet wird nichts von selbst.

## Zustände

- **Läuft:** der letzte Lauf war erfolgreich.
- **Fehler** und **Zeitüberschreitung:** der Dienst antwortete nicht wie erwartet; der Grund steht dabei.
- **Hängt** und **Abgestürzt:** ein Fehler des Moduls; der Agent und andere Module laufen weiter, das Modul startet
  nach einer Pause neu.
- **Konfiguration abgelehnt** und **Vom Host verboten:** die Einstellung oder die `agent.toml` des Hosts lassen es
  nicht laufen.
