# 0008 – Mitglieder per Einladungslink, SMTP optional

- Status: accepted
- Datum: 2026-11-19
- Bezug: KONZEPT.md §7 (Mandanten, User und Rechte), D28, O13; IMPLEMENTIERUNGSPLAN M1-04 (Teil c), E2.4

## Kontext

Org-Admins sollen neue Mitglieder in ihre Org holen. User sind instanzweite Identitäten (ein User, mehrere Orgs).
Dabei gilt:

- **Keine User-Enumeration:** Ein Org-Admin darf nicht erfahren, ob eine E-Mail-Adresse schon ein Konto hat. Sonst
  ließe sich herausfinden, wer die Instanz noch nutzt.
- **Kein Passwort durch Dritte:** Ein Startpasswort, das der Admin festlegt, wäre ihm bekannt und müsste über einen
  Nebenkanal weitergegeben werden.
- **Einfacher Betrieb:** Eine frische Installation hat oft noch keinen Mailserver. E-Mail-Versand kommt erst mit dem
  Notifier-Framework (E2.4).

## Optionen

1. **Anlegen mit Startpasswort.** Einfach, aber der Admin kennt das Passwort. Bei einer schon vergebenen Adresse
   müsste die Antwort abweichen, und das verrät, dass es das Konto gibt.
2. **Einladung nur per Mail.** Sauber, macht SMTP aber zur Pflicht.
3. **Einladungslink, Zustellung per Mail optional** (gewählt).

## Entscheidung

Der Admin lädt eine E-Mail-Adresse mit einer Startrolle ein (Gruppe oder alle Gruppen). Der Server erzeugt eine
Einladung:

- Token aus 256 Bit Zufall; gespeichert wird nur der SHA-256-Hash. 72 h gültig, nur einmal verwendbar.
- Gebunden an Org, E-Mail-Adresse und Startrolle; widerrufbar; Eintrag im Audit-Log.
- **Die Antwort ist immer gleich**, egal ob die Adresse schon ein Konto hat.

Zustellung:

- **Immer:** Die UI zeigt den Link dem Admin einmalig zum Kopieren. Danach ist er nicht mehr abrufbar, wie bei den
  Enrollment-Tokens.
- **Mit E-Mail-Kanal (ab E2.4):** Zusätzlich Versand an die eingeladene Adresse. **SMTP ist nie Pflicht.**

Das Token steht im Fragment des Links (`/invite#<token>`). So landet es weder in Server- und Proxy-Logs noch im
Referer.

Annahme:

- **Kein Konto:** Name und Passwort festlegen (Passwortregeln aus M1-02). Konto und Mitgliedschaft entstehen in einer
  Transaktion.
- **Konto vorhanden:** Mit genau dieser E-Mail-Adresse anmelden, dann entsteht die Mitgliedschaft.
- Registrierung mit einer schon vergebenen Adresse schlägt mit derselben Meldung fehl wie ein ungültiger Link. Die
  Einladung wird dabei nicht verbraucht.

Weitere Regeln:

- Annahme-Endpunkte sind pro IP rate-limitiert.
- Offene Einladungen kann der Admin auflisten und widerrufen.
- Abgelaufene Einladungen löscht ein Aufräumjob.

## Konsequenzen

- Mitglieder lassen sich ohne Mailserver einladen; die Einrichtung bleibt einfach.
- Der Admin sieht nie Passwörter. Die Einladungs-API verrät nicht, ob ein Konto existiert.
- **Restrisiko:** Wer den Link hält, kann bei der Annahme erkennen, ob die Adresse schon ein Konto hat. Das ist
  die eingeladene Person, beim Kopieren auch der Admin. Er erfährt nur, dass es das Konto gibt, nicht in welchen
  Orgs. Bewusst akzeptiert: Der Admin kennt die Adresse ohnehin. Rate-Limit und Audit-Log begrenzen Missbrauch.
- **Restrisiko (Nachtrag M1-35): Konto-Squatting.** Ohne Mailversand beweist niemand, dass die annehmende Person die
  Adresse besitzt. Ein Org-Admin kann die eigene Einladung an `opfer@example.org` selbst annehmen und so ein
  instanzweites Konto mit dieser Adresse und einem Passwort seiner Wahl anlegen; die echte Inhaberin kann danach keine
  Einladung einer anderen Org mehr annehmen. Gegenmaßnahme folgt mit M1-39 (Adressnachweis oder unbestätigte Konten,
  einheitliche Antwort bei der Annahme).
- Eine neue Tabelle `invitations` (RLS mit ENABLE + FORCE). Die Annahme läuft ohne Mandant über eine minimale
  `SECURITY DEFINER`-Funktion (`sermon_definer`, ADR 0007), die nur über den Token-Hash sucht.
- Passwort-Reset ohne SMTP kann später demselben Muster folgen: ein einmaliger Link, den ein Admin erzeugt.
