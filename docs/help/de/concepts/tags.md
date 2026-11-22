# Tags

Tags sind freie Paare `key=value` an einem Node, etwa `env=prod` oder `role=db`. Sie ordnen Nodes quer zu den Gruppen: Eine Gruppe bestimmt die Rechte, Tags nie.

## Wofür

- **Filter:** Die Flottenübersicht filtert nach einem Tag, die Suche findet Tags.
- **Regeln:** Der Selektor einer Regel kann Tags verlangen (`selector: { tags: { env: prod } }`); dann gilt sie nur für Nodes mit allen genannten Tags.
- **Benachrichtigungen:** Notification-Policies und Silences können auf Tags des Nodes passen, z. B. „nur `env=prod` nachts anrufen“ oder „`role=backup` während der Migration stumm“.

## Regeln für Tags

- Schlüssel: Kleinbuchstaben, Ziffern, `_` und `-`, beginnend mit einem Buchstaben, höchstens 32 Zeichen.
- Werte: Buchstaben, Ziffern und `_ . : / -`, beginnend mit einem Buchstaben oder einer Ziffer, höchstens 63 Zeichen.
- Höchstens 16 Tags pro Node, jeder Schlüssel einmal.

Tags setzen Admins der Gruppe des Nodes, mit einer frischen Bestätigung des Passworts; jede Änderung steht mit altem und neuem Stand im Audit-Log. Entfernt man ein Tag, enden die Alerts von Regeln, die es verlangen.
