# 0022 – Betriebsmodus und Einladungen nur per Mail

- Status: accepted
- Datum: 2026-11-20
- Bezug: KONZEPT.md §2 (D28), §7, §18; ADR 0008 (ersetzt dort „SMTP optional“); IMPLEMENTIERUNGSPLAN M1-35, M1-39

## Kontext

ADR 0008 ließ Einladungen ohne Mailserver zu: Die UI zeigte dem Admin den Link zum Weitergeben. Das Security-Review
M1-35 fand zwei Folgen:

- **Konto-Squatting:** Niemand beweist, dass die annehmende Person die Adresse besitzt. Ein Org-Admin konnte die eigene
  Einladung an `opfer@example.org` selbst annehmen. So entstand ein instanzweites Konto mit fremder Adresse und einem
  Passwort seiner Wahl; die echte Inhaberin hätte keiner anderen Org mehr beitreten können.
- **Konto-Enumeration:** Wer den Link hält, sieht bei der Annahme, ob es zur Adresse schon ein Konto gibt (auch
  instanzweit, also Orgs übergreifend).

Beides entsteht nur, weil der Link beim Admin landet. Ein Mailserver ist für Notifications (E2.4) ohnehin vorgesehen,
für die Entwicklung aber lästig. Bisher gab es keinen expliziten Betriebsmodus; Entwicklung und Produktion
unterschieden sich nur über einzelne Werte (z. B. `http://`-Origins ohne HSTS).

## Optionen

1. **Wie bisher, Restrisiko dokumentieren** – einfach, lässt Squatting aber zu.
2. **Unbestätigte Konten:** Link weiter beim Admin, das neue Konto bleibt bis zur Bestätigung per Mail gesperrt – zwei
   Mails, mehr Zustände, und ohne SMTP bleibt das Problem.
3. **Link nur per Mail, SMTP in Produktion Pflicht, Betriebsmodus** – der Link erreicht nur die Mailbox; ohne SMTP
   nur im ausdrücklichen Entwicklungsmodus.

## Entscheidung

Gewählt: **Option 3.**

- `SERMON_MODE=production|development`, **Standard `production`**. Wer die Variable vergisst, bekommt die strengen
  Regeln. `just dev` setzt `development`; der Server warnt beim Start deutlich.
- **Produktion:**
  - Mit Datenbank ist SMTP Pflicht (`SERMON_SMTP_HOST`, `_PORT`, `_FROM`, `_USERNAME`, `_PASSWORD`, `_TLS`, `_CA_FILE`).
  - Die Verbindung ist immer geschützt: STARTTLS (Standard, Port 587; ein Server ohne STARTTLS wird abgelehnt, bevor
    Zugangsdaten oder Inhalt fließen) oder TLS ab dem ersten Byte (Port 465). Das Zertifikat wird geprüft, gegen die
    System-CAs oder `SERMON_SMTP_CA_FILE`.
  - Erlaubt sind nur `https://`-Origins.
  - Fehlt etwas, startet der Server nicht und nennt alle Fehler auf einmal, auch bei `sermon-server init`.
- **Einladungen und die erste Admin-Einladung einer neuen Org** gehen dann nur per Mail an die Adresse (deutsch und
  englisch, Sprache der Person unbekannt). Die API gibt kein Token zurück (`emailed` bzw. `invitation_emailed`). Wer
  annimmt, besitzt die Mailbox; damit sind Squatting und Enumeration durch den Admin ausgeschlossen. „Erneut senden“
  lädt dieselbe Adresse noch einmal ein (ersetzt die offene Einladung, neuer Link). Scheitert der Versand, antwortet
  die API mit `UNAVAILABLE`, ohne Details; das Log nennt den Grund.
- **Entwicklung:** SMTP optional, auch unverschlüsselt (Mailpit), und `http://`-Origins erlaubt. Ohne SMTP zeigt die
  UI den Link wie bisher; er beweist dann nichts. `just dev` schickt die Mails an Mailpit.
- Der Link zeigt auf den ersten Eintrag von `SERMON_ALLOWED_ORIGINS` (mit SMTP Pflicht).

## Konsequenzen

- Positiv: Die Adresse eines Kontos ist in Produktion immer bewiesen. Der Admin sieht nie einen Link, der sich für
  fremde Adressen nutzen ließe. Der Modus bündelt künftige Entwicklungs-Lockerungen an einer Stelle.
- Negativ / Risiken:
  - Produktion braucht einen Mailserver. Das widerspricht KONZEPT D28 („SMTP nie Pflicht“); D28 ist angepasst.
  - Ein vergessenes `SERMON_MODE=development` in Produktion schaltet den Nachweis ab. Dagegen helfen der Standard
    `production` und die Warnung beim Start.
  - Der Mailserver ist eine neue Abhängigkeit. Scheitert er, gibt es keine Einladungen, aber der Server läuft weiter.
- Security-Auswirkungen: T23 und T61 im Threat Model sind geschlossen. Mail-Header entstehen nur aus geprüften
  Werten; Zeilenumbrüche in Empfänger, Betreff oder Absender werden abgelehnt, nie maskiert. Das SMTP-Passwort steht
  nie in Fehlermeldungen.
- Folgearbeiten: Notifications per Mail (E2.4) nutzen denselben Adapter; Passwort-Reset (später) ebenfalls per Mail
  statt per Admin-Link. Die UI für neue Orgs (Superadmin) zeigt „gesendet“ statt eines Links, sobald es sie gibt.
