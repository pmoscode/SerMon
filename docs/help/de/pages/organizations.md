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
