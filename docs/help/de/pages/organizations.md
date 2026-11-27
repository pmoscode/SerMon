# Organisationen

Die Organisationen der Instanz, nur für Superadmins: Name, Zahl der Mitglieder und Nodes, ungefährer Speicherbedarf, Status und Kontingente. Daten der Organisationen – Nodes, Metriken, Alerts, Mitglieder – sieht ein Superadmin hier nicht; dafür braucht er eine Mitgliedschaft.

## Anlegen

Name und E-Mail des ersten Admins genügen. Der erste Admin bekommt eine Einladung per Mail und verwaltet die Organisation danach selbst; der Superadmin wird nicht Mitglied. Namen sind eindeutig, ohne Unterschied von Groß- und Kleinschreibung. Anlegen und Bearbeiten verlangen eine erneute Bestätigung.

## Kontingente

Statt der Werte der Instanz gelten für eine Organisation eigene Grenzen:

- **Nodes:** Weitere Enrollments werden abgelehnt.
- **Mitglieder:** zählt mit offenen Einladungen; weitere Einladungen und das Annehmen werden abgelehnt.
- **Obergrenze der Retention:** gilt, sobald Organisationen ihre Retention selbst einstellen können.
- **Samples pro Sekunde und Node, Serien pro Node:** Ein Agent darüber wird gebremst, neue Serien werden verworfen.

Leer heißt Standard der Instanz. Ein gesenktes Kontingent löscht nichts: Bestehendes läuft weiter, nur Neues wird abgelehnt. Die Admins der Organisation sehen den Stand unter Organisation → Kontingente. Neue Ingest-Grenzen gelten ab der nächsten Verbindung eines Agents.

## Speicher

Der Speicherbedarf ist geschätzt: Die Größe aller Metriken der Instanz wird nach dem Anteil der Organisation an den Serien verteilt, die der Security-Events nach dem Anteil an den Events. Komprimierte Daten lassen sich nicht genau einer Organisation zuordnen.

## Sperren und Löschen

Unter **Sperren oder löschen** stehen die Schritte, die der Status erlaubt. Jeder verlangt den Namen der Organisation, genau so geschrieben, und eine erneute Bestätigung; jeder landet im Audit-Log der Organisation und der Instanz.

- **Sperren:** ohne Frist. Mitglieder werden sofort abgemeldet und kommen nicht mehr hinein, Agents werden getrennt, Enrollment-Tokens und Einladungen gelten nicht mehr. Alerts und Benachrichtigungen ruhen. Zertifikate und Daten bleiben. Wer als Superadmin selbst in der Organisation angemeldet ist, wird ebenfalls abgemeldet.
- **Zum Löschen vormerken:** sperrt wie oben und löscht nach der Frist der Instanz (Standard 30 Tage, `SERMON_ORG_DELETION_GRACE`, mindestens 7 Tage).
- **Sperre aufheben:** alles läuft wieder an; eine vorgemerkte Löschung entfällt.
- **Sofort löschen:** nur bei einer gesperrten Organisation, frühestens 10 Minuten nach der Sperre (bis dahin können Hosts mit vorgehender Uhr noch Log-Zeilen schicken). Endgültig gelöscht werden Nodes, Metriken, Events, Alerts, Kanäle, das Audit-Log und alle Konten ohne weitere Mitgliedschaft; Konten von Superadmins bleiben.
- **Audit-Log herunterladen:** nur bei einer gesperrten Organisation, als JSON-Zeilen mit der Hash-Kette. Die erste Zeile beschreibt, wie sich die Kette nachrechnen lässt; die UI meldet, ob sie intakt ist.

Die Admins der Organisation bekommen eine Mail beim Sperren (mit dem Löschdatum), beim Aufheben und 7 Tage vor der Löschung.

## Konto auf Antrag löschen

Verlangt eine Person die Löschung ihres Kontos, löscht der Superadmin es hier: die Adresse zweimal eingeben, dann
erneut bestätigen. Sitzungen, Passwort, zweiter Faktor, Passkeys und SSO-Verknüpfungen gehen sofort mit; private
Dashboards ebenso. Jede Organisation des Kontos bekommt einen Eintrag im Audit-Log, das Instanz-Audit-Log den Antrag;
dort bleibt nur die Kennung des Kontos, keine Adresse. Konten von Superadmins und der letzte Admin für alle Gruppen
einer Organisation lassen sich so nicht löschen – dort zuerst einen anderen Admin einsetzen.

Konten ohne Mitgliedschaft löscht SerMon ohnehin nach einer Frist (Standard 30 Tage,
`SERMON_ACCOUNT_DELETION_GRACE`); mehr unter [Konto](help:pages/account).
