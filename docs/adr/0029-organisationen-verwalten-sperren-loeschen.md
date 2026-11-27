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

## Nachtrag M2-39

- `org_purge` sperrt die Org-Zeile, bevor es Sperre und Frist prüft (Migration 00038): Ein gleichzeitiges „Sperre
  aufheben“ ist entweder vorher committet und wird gesehen, oder es wartet und findet die Org nicht mehr. Vorher konnte
  der Löschjob eine gerade entsperrte Org löschen. Die Konten ohne weitere Mitgliedschaft werden am Ende erneut
  geprüft.

## Nachtrag M3-10: Loki-Daten beim Löschen

- **Reihenfolge:** Das Löschen einer Organisation trägt zuerst bei Loki die Löschung ihres ganzen Tenants ein
  (Delete-API des Compactors, `{node_id=~".+"}`), danach löscht es die Stammdaten. Lehnt Loki ab oder ist es nicht
  erreichbar, bleibt die Organisation fällig. Der nächste Lauf der Wartung (stündlich) versucht es erneut, auch nach
  „Sofort löschen“.
- **Was danach geschieht, macht Loki:**
  - Der Compactor führt die Löschung nach der Wartefrist aus, für SerMon 10 Minuten statt 24 h: SerMon bricht keine
    Löschung ab.
  - Abfragen filtern die Zeilen, sobald die Querier ihre Liste neu laden (fest 5 Minuten). Diese Organisation fragt
    danach ohnehin niemand mehr ab.
  - Als Rückfall löscht die Retention nach 14 Tagen alles.
- **Abweichung – Nodes:** Einzelne Nodes lassen sich noch nicht löschen. Der Client kann die Zeilen eines Nodes
  löschen (`logql.MustSelect`, im Integrationstest geprüft); sobald es das Löschen von Nodes gibt, ruft es das auf.
- **Abweichung – Retention:** Die Obergrenze aus den Kontingenten gilt für Logs genauso wenig wie für Metriken, solange
  Organisationen ihre Retention nicht selbst einstellen. Alle haben die 14 Tage der Instanz. Später bietet Loki dafür
  `retention_period` pro Tenant (Runtime-Overrides).

## Nachtrag M5-30: Retention pro Organisation

- **Was:** Admins aller Gruppen (`manage_retention`, Step-up, Audit `org.retention_set`) verkürzen die Aufbewahrung der
  **Logs** (1 bis 14 Tage) und der **Security-Events mit der Alert-Historie** (7 bis 365 Tage). Leer heißt: wie die
  Instanz (14 Tage bzw. ein Jahr). Länger als die Instanz geht nicht: Loki löscht nach 14 Tagen alles, die Partitionen
  der Events nach einem Jahr. Unter 7 Tage für Events nicht, weil Agents Events so lange puffern.
- **Obergrenze:** `retention_cap_days` aus den Kontingenten begrenzt, was eingestellt wird. Wie bei allen Kontingenten
  löscht eine gesenkte Obergrenze nichts: eine bestehende Einstellung und der Standard der Instanz bleiben.
- **Events:** Der Server löscht stündlich je Org mit eigener Frist die älteren Zeilen (`security_events_expire_org`,
  Definer; nie etwas der letzten 7 Tage, gemessen an der Uhr der Datenbank). Das Löschen der Monatspartitionen nach
  einem Jahr bleibt.
- **Logs:** Loki kennt Retention pro Tenant nur über eine Runtime-Konfiguration (`overrides: <tenant>: retention_period`),
  die als Datei neben Loki liegt. Der Server müsste sie schreiben, mit einem gemeinsamen Volume zwischen Server und Loki,
  einer Datei, die beim ersten Start fehlt, und mehreren Server-Instanzen, die dieselbe Datei schreiben. Deshalb
  stattdessen die Delete-API von M3-10: einmal am Tag je Org eine Löschung `{node_id=~".+"}` vom Anfang der
  Instanz-Retention bis zur Frist der Org (`logs_erased_at`); Loki führt sie nach seiner Wartefrist (10 min) aus und
  markiert Anfragen außerhalb seiner Retention selbst als erledigt. **Grenze:** Logs bleiben bis zu einem Tag länger
  sichtbar als eingestellt; die Delete-Anfragen bleiben in Lokis Speicher (eine je Org und Tag).
- **Nicht pro Org:** Metriken (Continuous Aggregates mit Policies der Instanz), Audit-Log, Protokoll der
  Benachrichtigungen (30 Tage) und bekannte Login-Quellen (ein Jahr).

## Nachtrag M5-33: Untergrenze in der Datenbank (Security-Review)

- `security_events_expire_org` liest die Frist der Org selbst (`event_retention_days`) und löscht nur, was älter ist,
  gemessen an der Uhr der Datenbank – egal welche Zeit der Aufrufer übergibt (falsche Uhr, Fehler). Eine Org ohne
  eigene Frist bleibt unberührt (die Partitionen der Instanz löschen nach einem Jahr). Vorher galt nur die Untergrenze
  von 7 Tagen, auch für Orgs mit längerer oder ohne eigene Frist (die Alert-Historie hat für den Definer nicht den
  Schutz von 365 Tagen, den `sermon_app` hat; die Frist der Org ersetzt ihn).
- Die Funktion läuft nur im Tenant der übergebenen Org (`sermon.org_id`), sie setzt ihn nicht mehr selbst.
- **Obergrenze:** Eine gesenkte `retention_cap_days` löscht weiterhin nichts (wie oben): Eine bestehende längere
  Einstellung bleibt, bis ein Admin sie ändert. Die Obergrenze begrenzt das Einstellen, nicht das Behalten.

## Nachtrag V1-22: Vorgabe der Instanz für Events

- Ohne eigene Frist gilt für Security-Events und Alert-Historie nicht mehr ein Jahr, sondern `SERMON_EVENT_RETENTION`
  (ab Werk 90 Tage, ganze Tage von 7 bis 365; [ADR 0018](0018-retention.md), Nachtrag V1-22). Eine Org darf damit auch
  **länger** behalten als die Vorgabe, bis 365 Tage (und bis zu ihrer Obergrenze).
- Der Job löscht stündlich für jede Org, auch ohne eigene Frist: `security_events_expire_org(p_org, p_before,
  p_default_days)` nimmt die Frist der Org, sonst die Vorgabe, nie unter 7 Tagen, gemessen an der Uhr der
  Datenbank. Die Monatspartitionen gehen erst, wenn keine Org sie mehr behält (die längste Frist aller Orgs und die
  Vorgabe).
- Die Obergrenze bleibt eine Grenze für das Einstellen: Eine Org mit `retention_cap_days` unter der Vorgabe behält
  ohne eigene Frist die Vorgabe.
