# Benachrichtigungen

Wohin Alerts gemeldet werden: Kanäle, die Policies, die Alerts zu ihnen leiten, Wartungsfenster und das Zustellprotokoll. Verwalten dürfen das nur Admins der Organisation, und Änderungen fragen noch einmal nach dem Passwort.

## Kanäle

Ein Kanal ist ein Ziel wie E-Mail, ein Chat-Webhook oder ein Push-Dienst. Wie man URL oder Token bekommt, erklärt die Hilfe des jeweiligen Typs (ⓘ neben dem Typ). Zugangsdaten sind nur schreibbar: SerMon speichert sie verschlüsselt und zeigt sie nie wieder an; beim Bearbeiten behält ein leeres Feld den gespeicherten Wert. **Test senden** stellt sofort eine Testnachricht zu und zeigt den Fehler, wenn es scheitert (wenige Tests pro Minute).

## Policies

Eine Policy entscheidet, welche Alerts an welche Kanäle gehen:

- **Welche Alerts:** Stufen, Gruppen des Nodes, Regel-Muster und die Labels der Regel. Muster nutzen `*` für beliebige Zeichen, z. B. `security.*` oder `system.cpu.*`; Labels sind `key=value`-Zeilen, z. B. `category=security`. Leer heißt alle; jedes ausgefüllte Feld muss passen.
- Alle passenden Policies greifen, aber ein Kanal erfährt eine Änderung nur einmal.
- **Gruppierung:** pro Node (mehrere Alerts eines Nodes in einer Runde werden eine Nachricht) oder als Sammelnachricht aller Alerts einer Runde.
- **Wiederholen:** Ein feuernder Alert, den niemand quittiert, geht nach dieser Zeit erneut raus (0: nie).
- **Eskalation:** Nach den angegebenen Minuten ohne Quittierung bekommen weitere Kanäle den Alert und ab dann alles dazu, auch sein Ende. Quittieren stoppt Wiederholung und Eskalation.

Ohne eine einzige Policy gehen alle Alerts an alle aktiven Kanäle. Sobald es eine Policy gibt – auch eine abgeschaltete –, gelten nur noch die Policies.

## Wartungsfenster

Jede Woche an den gewählten Tagen, ab der Startzeit für die angegebene Dauer in der gewählten Zeitzone, sind die Alerts „Node offline“ und „Reboot“ der gewählten Gruppen (keine Auswahl: alle Gruppen) stumm. Andere Alerts melden weiter. Für einmalige Arbeiten gibt es Silences auf der Alert-Seite.

## Protokoll

Jede Zustellung der letzten 30 Tage mit ihrem Status. Fehler nennen den Host des Ziels, nie Zugangsdaten.
