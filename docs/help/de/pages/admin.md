# Organisation

Verwaltung der Organisation für Admins aller Gruppen: Mitglieder und ihre Rollen, Einladungen, Node-Gruppen, die Pflicht zur Zwei-Faktor-Anmeldung und das Audit-Log.

## Rollen

- **Betrachter** sehen Nodes, Metriken und (sofern erlaubt) Logs.
- **Operatoren** dürfen zusätzlich Nodes hinzufügen und später Alerts quittieren, Updates und Reboots auslösen.
- **Admins** einer Gruppe verwalten diese Gruppe; Admins für **alle Gruppen** die ganze Organisation.

Eine Rolle gilt für eine Gruppe oder für alle Gruppen. Mehrere Rollen ergänzen sich.

## Gut zu wissen

- Rechte ändern, Mitglieder entfernen, Gruppen löschen und Einladen verlangen eine erneute Bestätigung (Passwort oder zweiter Faktor).
- Einladungen sind Links, die nur einmal angezeigt werden; SerMon verschickt keine E-Mails.
- Der letzte Admin für alle Gruppen lässt sich nicht entfernen.
- Jede Änderung steht mit Zeit und Person im Audit-Log; die Einträge sind verkettet und lassen sich nicht unbemerkt ändern.
