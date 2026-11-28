# Organisation

Verwaltung der Organisation für Admins aller Gruppen: Mitglieder und ihre Rollen, Einladungen, Node-Gruppen, die Pflicht zur Zwei-Faktor-Anmeldung und das Audit-Log.

## Rollen

- **Betrachter** sehen Nodes, Metriken und (sofern erlaubt) Logs.
- **Operatoren** dürfen zusätzlich Nodes hinzufügen, Alerts quittieren und stummschalten, Updates und Reboots auslösen und gesperrte Nodes freigeben.
- **Admins** einer Gruppe verwalten diese Gruppe; Admins für **alle Gruppen** die ganze Organisation.

Eine Rolle gilt für eine Gruppe oder für alle Gruppen. Mehrere Rollen ergänzen sich. Im Einzelnen: [Rollen und Rechte](help:concepts/roles).

## Logs

Der Reiter **Logs** legt fest, was die Agents jeder Gruppe senden, ob Viewer Logs sehen und welche eigenen Redaction-Regeln gelten; er zeigt auch, welche Einstellungen jeder Agent angewendet hat. Mehr dazu: [Log-Einstellungen](help:concepts/log_settings).

## Module

Der Reiter **Module** schaltet je Gruppe die Module ein, die die Agents ihrer Nodes kennen, und richtet sie ein; darunter stehen die ausgeblendeten Vorschläge der Gruppe mit **Wieder anzeigen**. Mehr dazu: [Module](help:concepts/modules).

## SSO-Gruppen

Hat die Instanz einen OIDC-Provider, ordnet der Reiter **SSO-Gruppen** Gruppen des Providers einer Rolle zu. Bei jeder Anmeldung folgen diese Rollen den Gruppen; in der Mitgliederliste tragen sie das Zeichen „SSO“. Mehr dazu: [SSO-Gruppen](help:concepts/oidc-group-mappings).

## Gut zu wissen

- Rechte ändern, Mitglieder entfernen, Gruppen löschen und Einladen verlangen eine erneute Bestätigung (Passwort oder zweiter Faktor).
- Einladungen gehen per E-Mail an die eingeladene Adresse; nur wer das Postfach hat, kann sie annehmen. Der Link gilt einmal innerhalb von 72 Stunden. „Erneut senden“ schickt einen neuen Link, der alte wird ungültig. Nur auf einem Entwicklungsserver ohne Mail zeigt SerMon den Link einmal zum Weitergeben. Abgelaufene Einladungen löscht SerMon mit ihrer Adresse; das Audit-Log nennt nur die Einladung, nicht die Adresse.
- Ein entferntes Mitglied ohne weitere Organisation verliert sein Konto nach einer Frist der Instanz (Standard 30 Tage); wird es vorher wieder eingeladen, bleibt das Konto.
- Der letzte Admin für alle Gruppen lässt sich nicht entfernen.
- Jede Änderung steht mit Zeit und Person im Audit-Log; die Einträge sind verkettet und lassen sich nicht unbemerkt ändern.
- Der Reiter **Kontingente** zeigt die Grenzen, die der Superadmin der Instanz gesetzt hat, und wie viel davon belegt ist. An der Grenze lehnt SerMon neue Nodes und Einladungen ab; Bestehendes läuft weiter.
