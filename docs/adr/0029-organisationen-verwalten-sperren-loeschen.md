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
