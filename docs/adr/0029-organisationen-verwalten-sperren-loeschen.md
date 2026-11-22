# 0029 – Organisationen verwalten, sperren und löschen

- Status: accepted
- Datum: 2026-11-22
- Bezug: KONZEPT.md §2 (D29), §7, §24 (O14); ADR 0008, ADR 0017, ADR 0022; IMPLEMENTIERUNGSPLAN M2-32, M2-33, E3.8

## Kontext

Superadmins legen Organisationen seit M1-04 über die API an und benennen sie um; eine Oberfläche dafür fehlt. D29
legt fest, dass Organisationen erst gesperrt und nach einer Frist gelöscht werden, ließ aber offen, wer das darf, wie
man es bestätigt, was eine Sperre bewirkt und was mit Konten und dem Audit-Log geschieht. Für einen späteren Betrieb
mit vielen Organisationen fehlen Kontingente. Die Fragen hat der Product Owner am 2026-11-22 beantwortet.

## Entscheidung

**Verwalten (M2-32):**

- Nur **Superadmins** sehen die Organisationsverwaltung. Die Liste zeigt ohne Mitgliedschaft nur Zahlen und Status:
  Name, angelegt am, Zahl der Mitglieder und Nodes, ungefährer Speicherbedarf (Metriken, Events), Status (aktiv,
  gesperrt, Löschung am …). Keine Namen von Nodes oder Usern.
- **Anlegen** wie bisher: mit Step-up, der erste Admin wird per Mail eingeladen; der Superadmin wird nicht Mitglied.
- **Namen sind eindeutig**, ohne Unterscheidung von Groß- und Kleinschreibung.
- **Bearbeiten:** Der Superadmin ändert den Namen und die **Kontingente** der Organisation: höchstens so viele Nodes,
  Mitglieder, eine Obergrenze der Retention und Ingest-Grenzen (Samples pro Sekunde, Serien pro Node) statt der
  instanzweiten Werte. Inhaltliche Einstellungen (2FA-Pflicht, Logs für Viewer) bleiben beim Admin der Organisation.
- **An der Grenze** wird Neues abgelehnt, Bestehendes läuft weiter: neue Nodes beim Enrollment, Einladungen, Retention
  über der Obergrenze, Samples über der Ingest-Grenze. Die Meldung nennt das Kontingent, die Admins der Organisation
  sehen den Stand. Ein gesenktes Kontingent löscht nichts.

**Sperren und löschen (M2-33, präzisiert D29):**

- Nur **Superadmins**, jeweils mit **Step-up und Eingabe des Namens** der Organisation.
- **Zwei Aktionen:** „Sperren“ (ohne Frist, bis ein Superadmin aufhebt) und „Zum Löschen vormerken“ (sperrt und
  startet die Frist). Die Frist ist **30 Tage**, einstellbar über die Server-Konfiguration mit einer Untergrenze von
  7 Tagen. Während der Frist kann ein Superadmin die Sperre **aufheben** oder **sofort endgültig löschen** (etwa auf
  Wunsch der Organisation).
- **Eine Sperre beendet sofort** Sitzungen und Logins, Agent-Verbindungen und Ingest, und sie macht
  Enrollment-Tokens unbrauchbar. Die Agent-Zertifikate bleiben bis zum Löschen gültig, damit ein Aufheben ohne
  Neuinstallation geht; die Agents puffern lokal. **Alerts und Benachrichtigungen ruhen** (keine Welle von
  „Node offline“).
- **Mails an die Admins der Organisation:** bei Sperre und Vormerken (mit Datum der Löschung), beim Aufheben und
  7 Tage vor dem endgültigen Löschen.
- **Audit-Log:** Während der Sperre lädt ein Superadmin es als Datei mit Hash-Kette herunter; der Download steht im
  Instanz-Audit, weil der Superadmin dabei Daten der Organisation sieht.
- **Endgültiges Löschen** entfernt Stamm-, Event- und Metrikdaten der Organisation (Loki-Daten, sobald es sie gibt,
  E3.2) und **Konten ohne weitere Mitgliedschaft**; Konten mit anderen Organisationen und Superadmins behalten ihr
  Konto, nur die Mitgliedschaft endet.
- **Keine Sonderregel:** Auch die letzte Organisation lässt sich löschen; Superadmins hängen an keiner Organisation.

## Konsequenzen

- Positiv: Der Ablauf nach D29 ist vollständig festgelegt, und M2-33 kann ihn vor der M2-Demo umsetzen statt erst in
  M3. Kontingente bereiten einen Betrieb mit vielen Organisationen vor.
- Negativ: Beim Audit-Export sieht der Superadmin Daten der Organisation; das ist eine bewusste Ausnahme und wird
  protokolliert.
- Negativ: Der Speicherbedarf pro Organisation ist bei komprimierten Hypertables nur zu schätzen; M2-32 legt das
  Verfahren fest.
- Die Obergrenze der Retention greift erst, wenn Organisationen ihre Retention selbst einstellen können; bis dahin
  gilt die Retention der Instanz.
- E3.8 schrumpft auf das Löschen der Loki-Daten (mit E3.2).

## Nachtrag M2-32

- **Speicherbedarf:** Die Größe aller Metrik-Hypertables einschließlich der Continuous Aggregates wird nach dem Anteil
  der Organisation an den Serien verteilt, die Größe der Partitionen der Security-Events nach ihrem Anteil an den
  Events. Die Gesamtgrößen liefert eine Definer-Funktion (nur Größen, keine Zeilen); Serien, Events und Nodes zählt der
  Server pro Organisation in deren Tenant-Transaktion. Eine Definer-Policy auf `series` gibt es bewusst nicht, weil
  die Leser der Metriken sich auf die Mandanten-Policy verlassen.
- **Mitglieder-Kontingent** zählt offene Einladungen mit; eine erneute Einladung derselben Adresse ersetzt die alte.
  Beim Annehmen wird noch einmal gezählt (das Kontingent kann inzwischen gesunken sein), die Einladung bleibt dann.
- **Ingest-Kontingente** gelten ab der nächsten Session eines Agents; der Burst behält das Verhältnis der Instanz.
- **Ort der Seite:** eigener Menüpunkt „Organisationen“ für Superadmins, nicht im Admin-Bereich der Org, den ein
  Superadmin ohne Mitgliedschaft nicht sieht.

## Nachtrag M2-33

- **Sperre über die Definer-Funktionen:** Die Funktionen, die Agents, Enrollment-Tokens, Einladungen und die Orgs beim
  Login zulassen, prüfen `locked_at IS NULL`; die Mitgliedschaftsprüfung der Sitzungen ebenso. Damit wirkt die Sperre
  auf allen Server-Instanzen, ohne dass sie voneinander wissen. Die eigene Instanz löscht zusätzlich die Sitzungen der
  Org und beendet ihre Agent-Streams sofort; andere Instanzen trennen die Agents bei der Prüfung des Zertifikats
  (spätestens nach einer Minute).
- **Sofort löschen** setzt nur bei einer gesperrten Org die Frist auf jetzt und löscht in derselben Anfrage; ein
  Superadmin muss also immer erst sperren. Der Export ist ebenso nur während der Sperre möglich.
- **Name exakt:** Die Bestätigung unterscheidet Groß- und Kleinschreibung (Leerzeichen am Rand zählen nicht), obwohl
  Namen ohne diesen Unterschied eindeutig sind – wer bestätigt, soll den Namen bewusst abschreiben.
- **Export:** JSON-Zeilen; die erste Zeile beschreibt Org, Zeitpunkt, Zahl der Einträge, ob die Kette intakt ist, und die
  Regel des Hashes. `details` steht als gespeicherter Text in der Datei, damit sich jeder Hash aus der Datei allein
  nachrechnen lässt.
- **Löschen in einer Transaktion** (`org_purge`, Definer): Metriken und Continuous Aggregates über die Serien der Org,
  Events, Audit-Log, Nodes, die Org mit allen abhängigen Zeilen, zuletzt die Konten ohne weitere Mitgliedschaft
  (Superadmins ausgenommen). Der Eintrag `org.delete` im Instanz-Audit entsteht in derselben Transaktion.
- **Löschjob** stündlich in jeder Server-Instanz; `org_purge` lehnt ab, was nicht gesperrt und fällig ist, und die
  Erinnerung wird über `deletion_reminded_at` genau einmal beansprucht.
