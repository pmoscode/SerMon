# Organisation

Verwaltung der Organisation für Admins aller Gruppen: Mitglieder und ihre Rollen, Einladungen, Node-Gruppen, die Pflicht zur Zwei-Faktor-Anmeldung und das Audit-Log.

## Rollen

- **Betrachter** sehen Nodes, Metriken und (sofern erlaubt) Logs.
- **Operatoren** dürfen zusätzlich Nodes hinzufügen und später Alerts quittieren, Updates und Reboots auslösen.
- **Admins** einer Gruppe verwalten diese Gruppe; Admins für **alle Gruppen** die ganze Organisation.

Eine Rolle gilt für eine Gruppe oder für alle Gruppen. Mehrere Rollen ergänzen sich.

## Gut zu wissen

- Rechte ändern, Mitglieder entfernen, Gruppen löschen und Einladen verlangen eine erneute Bestätigung (Passwort oder zweiter Faktor).
- Einladungen gehen per E-Mail an die eingeladene Adresse; nur wer das Postfach hat, kann sie annehmen. Der Link gilt einmal innerhalb von 72 Stunden. „Erneut senden“ schickt einen neuen Link, der alte wird ungültig. Nur auf einem Entwicklungsserver ohne Mail zeigt SerMon den Link einmal zum Weitergeben.
- Der letzte Admin für alle Gruppen lässt sich nicht entfernen.
- Jede Änderung steht mit Zeit und Person im Audit-Log; die Einträge sind verkettet und lassen sich nicht unbemerkt ändern.
- Der Reiter **Kontingente** zeigt die Grenzen, die der Superadmin der Instanz gesetzt hat, und wie viel davon belegt ist. An der Grenze lehnt SerMon neue Nodes und Einladungen ab; Bestehendes läuft weiter.
