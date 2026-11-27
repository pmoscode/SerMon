# Rollen und Rechte

Was Betrachter, Operatoren und Admins dürfen. Eine Rolle gilt für eine Node-Gruppe oder für alle Gruppen der
Organisation; mehrere Rollen ergänzen sich, und wer keine Rolle auf einer Gruppe hat, sieht ihre Nodes nicht.

## Betrachter

- Nodes, Metriken, Alerts, Updates und Dashboards der Gruppe sehen.
- Logs und Sicherheitsereignisse lesen – solange die Organisation das Betrachtern nicht verbietet
  ([Log-Einstellungen](help:concepts/log_settings)).
- Die [Live-Ansicht](help:concepts/live-view) der Prozesse öffnen; die vollen Befehlszeilen nur, wenn die Organisation
  sie freigegeben hat.
- Eigene Dashboards anlegen; geteilte Dashboards ansehen. Jedes Widget zeigt nur, was die Rolle des Betrachters
  erlaubt.

## Operator

Alles wie Betrachter, dazu auf der Gruppe:

- Alerts quittieren und stummschalten.
- Updates und Reboots auslösen.
- Nodes hinzufügen (Enrollment-Token), Schlüssel eines Nodes erneuern, gesperrte Nodes freigeben und eine Übernahme
  erlauben ([Identität](help:concepts/node-identity)).

## Admin einer Gruppe

Alles wie Operator, dazu Tags der Nodes setzen ([Tags](help:concepts/tags)) sowie Nodes deaktivieren und löschen
([Lebenszyklus](help:concepts/node-lifecycle)).

## Admin für alle Gruppen

Verwaltet die ganze Organisation: Mitglieder, Einladungen und Gruppen, Alert-Regeln, Kanäle, Richtlinien und
Wartungsfenster, [Update-Richtlinien](help:concepts/update-policy), Module, Log-Einstellungen und Redaction,
[Aufbewahrung](help:concepts/retention), die Pflicht zur Zwei-Faktor-Anmeldung, geteilte Dashboards anderer,
Kiosk-Tokens und das Audit-Log.

## Superadmin

Der Betreiber der Instanz legt Organisationen an, sperrt und löscht sie, setzt ihre Kontingente und verwaltet die
Agent-CA. Ohne eigene Mitgliedschaft sieht er keine Daten einer Organisation.

## Gut zu wissen

- Rechte vergeben und entziehen, Mitglieder entfernen und Einladen verlangen eine
  [erneute Bestätigung](help:concepts/step-up).
- Rollen aus SSO-Gruppen folgen bei jeder Anmeldung den Gruppen beim Provider
  ([SSO-Gruppen](help:concepts/oidc-group-mappings)).
- Der letzte Admin für alle Gruppen lässt sich nicht entfernen.
