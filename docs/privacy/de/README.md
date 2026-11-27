# Vorlage: Datenschutz-Dokumentation für den Betrieb von SerMon

> **Vorlage, keine Rechtsberatung.** Dieses Dokument beschreibt, welche personenbezogenen Daten SerMon verarbeitet
> und welche Schutzmaßnahmen die Software mitbringt – so, wie sie im Code umgesetzt sind (Stand: v1.0). Es ersetzt
> weder die Prüfung durch eine Datenschutzbeauftragte oder einen Datenschutzbeauftragten noch eine rechtliche
> Beratung. Rechtsgrundlagen, Fristen und organisatorische Maßnahmen hängen von deinem Einsatz ab und sind hier nur
> vorgeschlagen. Stellen in **[eckigen Klammern]** sind auszufüllen.

English version: [GDPR documentation template](../en/README.md).

## Inhalt

1. [Rollen: wer ist verantwortlich?](#1-rollen-wer-ist-verantwortlich)
2. [Welche personenbezogenen Daten SerMon hält](#2-welche-personenbezogenen-daten-sermon-hält)
3. [Verzeichnis der Verarbeitungstätigkeiten (Art. 30 DSGVO)](#3-verzeichnis-der-verarbeitungstätigkeiten-art-30-dsgvo)
4. [Technische und organisatorische Maßnahmen (Art. 32 DSGVO)](#4-technische-und-organisatorische-maßnahmen-art-32-dsgvo)
5. [Löschfristen und Löschen](#5-löschfristen-und-löschen)
6. [Auftragsverarbeitung bei Mandanten (Art. 28 DSGVO)](#6-auftragsverarbeitung-bei-mandanten-art-28-dsgvo)
7. [Rechte der Betroffenen](#7-rechte-der-betroffenen)
8. [Bekannte Grenzen](#8-bekannte-grenzen)

## 1. Rollen: wer ist verantwortlich?

SerMon ist mandantenfähig: Eine Instanz (der **Betreiber**, mit Superadmins) hält eine oder mehrere
**Organisationen**. Jede Organisation verwaltet ihre Mitglieder, Nodes (überwachte Server) und Einstellungen selbst;
ein Superadmin sieht ohne eigene Mitgliedschaft keine Daten einer Organisation.

- **Eigener Betrieb** (Betreiber und einzige Organisation sind dieselbe Stelle): Der Betreiber ist Verantwortlicher.
- **Betrieb für andere** (z. B. ein Dienstleister betreibt die Instanz für Kunden, jeder Kunde ist eine Organisation):
  Jede Organisation ist für die Daten ihrer Server Verantwortliche; der Betreiber verarbeitet sie in ihrem Auftrag
  (Auftragsverarbeitung, Abschnitt 6). Für die Konten der Mitglieder und das Instanz-Audit-Log kann der Betreiber
  selbst Verantwortlicher sein – das ist im Vertrag festzulegen.

| Angabe | Eintrag |
|---|---|
| Verantwortliche Stelle (Name, Anschrift) | [auszufüllen] |
| Vertretung | [auszufüllen] |
| Datenschutzbeauftragte/r | [auszufüllen] |
| Betreiber der Instanz (falls abweichend) | [auszufüllen] |
| Adresse der Instanz | [z. B. `https://sermon.example.org`] |
| Standort von Server, Datenbank und Sicherungen | [Rechenzentrum, Land] |

## 2. Welche personenbezogenen Daten SerMon hält

Betroffen sind vor allem **Mitglieder** (Personen mit Konto in SerMon) und **Personen, die sich an den überwachten
Servern anmelden oder deren Handlungen in deren Logs stehen** (Administratoren, Benutzer der Dienste auf den Hosts,
Angreifer). Auch Hostnamen und IP-Adressen können personenbezogen sein, etwa bei Servern von Einzelpersonen.

| Kategorie | Daten | Herkunft | Ablage |
|---|---|---|---|
| Konto | E-Mail-Adresse, Anzeigename, Passwort-Hash (Argon2id), Zeitpunkt der Passwortänderung, Fehlversuche und Sperre, seit wann ohne Mitgliedschaft; eine angefragte neue E-Mail-Adresse mit dem Code nur als Hash (eine Stunde) | Einrichtung, Einladung, SSO, Mitglied | Datenbank |
| Anmeldefaktoren | TOTP-Seed (verschlüsselt), Recovery-Codes (Hash), Passkeys (öffentlicher Schlüssel, Name, letzte Nutzung), SSO-Identität (Issuer, Subject, E-Mail, letzte Anmeldung) | Mitglied, Identity Provider | Datenbank |
| Sitzungen | Sitzungs-ID, Beginn, letzte Aktivität, Zeit der letzten Bestätigung (keine IP-Adresse, kein User-Agent) | Anmeldung | Datenbank |
| Mitgliedschaft | Organisation, Rollen je Gruppe, Herkunft der Rolle (z. B. SSO-Gruppe) | Admins, Identity Provider | Datenbank |
| Einladungen | E-Mail-Adresse, Rolle, einladende Person, Ablauf (Token nur als Hash) | Admins | Datenbank, E-Mail |
| Audit-Log | Zeit, handelnde Person (ID), Aktion, Ziel, Details (z. B. neue IP eines Nodes; ab v1.0 keine E-Mail-Adressen); im Instanz-Audit-Log zusätzlich Änderungen, Export und Löschung von Konten (nur mit Kennung) | jede Änderung | Datenbank |
| Nodes | Hostname, `machine-id`, Betriebssystem, Kernel, Hardware, Tags, letzte IP-Adresse des Agents | Agent | Datenbank |
| Metriken | Messwerte des Hosts und seiner Dienste (CPU, Speicher, Platten, Netzwerk, Module); Labels wie Dateisystem, Netzwerkkarte, Unit- oder Containername | Agent | Datenbank (TimescaleDB) |
| Logs | Log-Zeilen aus dem systemd-Journal und aus freigegebenen Logdateien – mit allem, was darin steht (Benutzernamen, IP-Adressen, URLs, Inhalte); bekannte Geheimnisse vom Agent vor dem Versand ersetzt; für das Histogramm die Anzahl der Zeilen je Node, Quelle, Schweregrad und Minute (ohne Inhalte) | Agent | Loki; die Anzahlen in der Datenbank |
| Security-Events | SSH-Anmeldungen (Benutzername, Quell-IP oder -Host, Port, Methode, Schlüsseltyp und Fingerprint), sudo (Benutzer, Befehlszeile redigiert), angelegte und gelöschte Konten, Reboots, OOM-Kills, Enrollments und Adresswechsel der Agents (IP-Adresse); optional das Land der IP aus einer lokalen GeoIP-Datei | Agent | Datenbank |
| Bekannte Anmeldequellen | je Node: Benutzername, Quelle, erste und letzte Anmeldung, Zahl | aus den Security-Events | Datenbank |
| Alerts | aktive Alerts und Verlauf mit den Feldern der Regel – bei Sicherheitsregeln z. B. Benutzername und IP-Adresse | Alerting | Datenbank |
| Benachrichtigungen | Kanäle (Empfänger wie E-Mail-Adressen oder Chat-Ziele; Zugangsdaten verschlüsselt), Protokoll der Zustellungen mit dem Inhalt der Nachricht | Admins, Alerting | Datenbank, externe Dienste |
| Live-Ansicht der Prozesse | Prozesse mit Benutzer und Befehlszeile (redigiert, auf 1 KiB gekürzt, für die Organisation abschaltbar) | Agent, nur solange jemand zusieht | **nicht gespeichert** |
| Logs des Servers | Betriebsmeldungen; IP-Adresse bei abgelehnten Enrollments; keine Passwörter, Tokens oder Inhalte von Anfragen | Server | Container-Log des Betreibers |

Auf den Hosts puffert der Agent Messwerte und Events, solange der Server nicht erreichbar ist (Events bis zu 7 Tage,
`/var/lib/sermon-agent`).

## 3. Verzeichnis der Verarbeitungstätigkeiten (Art. 30 DSGVO)

Vorschlag für die Einträge. Je Tätigkeit sind Rechtsgrundlage, Empfänger und Fristen zu prüfen und anzupassen.
Die Maßnahmen stehen gesammelt in Abschnitt 4, die Fristen in Abschnitt 5.

### 3.1 Benutzerkonten und Zugriffsverwaltung

| | |
|---|---|
| Zweck | Anmeldung, Rechteverwaltung und Nachvollziehbarkeit des Zugriffs auf die Monitoring-Daten |
| Betroffene | Mitglieder, eingeladene Personen |
| Daten | Konto, Anmeldefaktoren, Sitzungen, Mitgliedschaft, Einladungen (Abschnitt 2) |
| Rechtsgrundlage | [z. B. Art. 6 Abs. 1 lit. b DSGVO (Vertrag) bzw. § 26 BDSG/Art. 88 DSGVO bei Beschäftigten] |
| Empfänger | Admins der Organisation; Mailserver für Einladungen; ggf. Identity Provider (SSO) |
| Drittland | [nein / Mailserver bzw. Identity Provider: …] |
| Frist | Konten bis 30 Tage nach dem Ende der letzten Mitgliedschaft (einstellbar) oder bis zum Antrag; Sitzungen 30 Minuten ohne Aktivität, höchstens 12 Stunden; Einladungen 72 Stunden, danach gelöscht |

### 3.2 Überwachung der Server (Metriken und Host-Daten)

| | |
|---|---|
| Zweck | Verfügbarkeit, Leistung und Kapazität der Server und Dienste überwachen, Störungen erkennen |
| Betroffene | in der Regel keine Personen; bei Servern von Einzelpersonen deren Inhaber |
| Daten | Nodes, Metriken (Abschnitt 2) |
| Rechtsgrundlage | [z. B. Art. 6 Abs. 1 lit. f DSGVO (berechtigtes Interesse am sicheren Betrieb)] |
| Empfänger | Mitglieder mit Rolle auf der Node-Gruppe; Kiosk-Anzeigen der Organisation |
| Frist | 10-Sekunden-Werte 24 Stunden, Minutenwerte 30 Tage, Stundenwerte 1 Jahr |

### 3.3 Sammlung und Auswertung von Logs

| | |
|---|---|
| Zweck | Fehlersuche und Nachvollziehen von Störungen und Sicherheitsvorfällen |
| Betroffene | Personen, die in den Logs der Hosts vorkommen (Administratoren, Benutzer der Dienste, Dritte) |
| Daten | Logs (Abschnitt 2) |
| Rechtsgrundlage | [z. B. Art. 6 Abs. 1 lit. f DSGVO; Art. 32 DSGVO] |
| Empfänger | Operatoren und Admins der Gruppe; Betrachter nur, wenn die Organisation es erlaubt |
| Frist | je Organisation 1 bis 14 Tage (ab Werk 14) |
| Steuerung | je Gruppe: Logs aus, Mindest-Schweregrad, ausgeschlossene Units, Logdateien; eigene Redaction-Regeln; auf dem Host `allow_logs = false` und erlaubte Verzeichnisse |

### 3.4 Sicherheitsereignisse auf den Servern

| | |
|---|---|
| Zweck | Angriffe und Missbrauch erkennen (z. B. SSH-Brute-Force, Anmeldung von neuer Quelle, Root-Login), Vorfälle aufklären |
| Betroffene | Personen, die sich an den Hosts anmelden oder es versuchen |
| Daten | Security-Events, bekannte Anmeldequellen, Alerts mit ihren Feldern (Abschnitt 2) |
| Rechtsgrundlage | [z. B. Art. 6 Abs. 1 lit. f DSGVO; Art. 32 DSGVO] |
| Empfänger | Mitglieder der Gruppe (Betrachter nur, wenn erlaubt); Empfänger der Benachrichtigungen (3.5) |
| Frist | je Organisation 7 bis 365 Tage (ab Werk 90, `SERMON_EVENT_RETENTION`); bekannte Quellen 1 Jahr nach der letzten Anmeldung |

### 3.5 Alarmierung und Benachrichtigungen

| | |
|---|---|
| Zweck | Zuständige über Störungen und Sicherheitsereignisse informieren |
| Betroffene | Empfänger der Benachrichtigungen; Personen, deren Benutzername oder IP-Adresse in einem Alert steht |
| Daten | Alerts, Kanäle, Zustellprotokoll (Abschnitt 2) |
| Rechtsgrundlage | [z. B. Art. 6 Abs. 1 lit. f DSGVO] |
| Empfänger | die Kanäle, die die Organisation einrichtet: Mailserver, Chat- und Push-Dienste (Slack, Microsoft Teams, Discord, Mattermost, Telegram, Gotify, ntfy, Pushover) oder eigene Webhooks |
| Drittland | [je Kanal prüfen; mehrere dieser Dienste verarbeiten Daten in den USA] |
| Frist | Verlauf der Alerts wie die Security-Events (3.4); Zustellprotokoll 30 Tage |

### 3.6 Audit-Log

| | |
|---|---|
| Zweck | Nachweis, wer wann was geändert hat (Rechte, Regeln, Kanäle, Updates, Löschungen …) |
| Betroffene | Mitglieder (nur mit ihrer Kennung); in älteren Einträgen eingeladene E-Mail-Adressen |
| Daten | Audit-Log (Abschnitt 2); für die Instanz ein eigenes Audit-Log der Superadmins |
| Rechtsgrundlage | [z. B. Art. 6 Abs. 1 lit. c/f DSGVO, Art. 5 Abs. 2 DSGVO (Rechenschaft)] |
| Empfänger | Admins der Organisation; Superadmins für das Instanz-Log und den Export einer gesperrten Organisation |
| Frist | 2 Jahre (`SERMON_AUDIT_RETENTION`, 90 Tage bis 10 Jahre), längstens bis die Organisation gelöscht wird |

### 3.7 Live-Ansicht der Prozesse

| | |
|---|---|
| Zweck | akute Fehlersuche auf einem Host („top“) |
| Betroffene | Benutzer, unter denen Prozesse laufen; Personen in Befehlszeilen |
| Daten | Prozessliste mit Benutzer und Befehlszeile (redigiert) |
| Empfänger | Mitglieder der Gruppe; Befehlszeilen nur für die, die Logs sehen dürfen, und nur, wenn die Organisation sie nicht ausblendet |
| Frist | keine Speicherung; der Agent sendet nur, solange die Ansicht offen ist (höchstens 15 Minuten am Stück) |
| Steuerung | auf dem Host `allow_live_view = false` |

## 4. Technische und organisatorische Maßnahmen (Art. 32 DSGVO)

Die Maßnahmen der Software sind hier beschrieben; die des Betriebs (Rechenzentrum, Zutritt, Personal, Sicherungen,
Patchen des Hosts) ergänzt der Betreiber.

### 4.1 Vertraulichkeit

**Zutritt** zu den Räumen und Systemen: [Maßnahmen des Rechenzentrums bzw. Betreibers].

**Zugang** (Anmeldung):

- Passwörter nur als Argon2id-Hash; nach fünf Fehlversuchen wird das Konto vorübergehend gesperrt (wachsend bis
  15 Minuten), ebenso bei falschen Codes.
- Zwei-Faktor-Anmeldung mit TOTP oder Passkeys (WebAuthn); je Organisation als Pflicht einstellbar. Anmeldung über
  einen Identity Provider (OIDC) möglich.
- Sitzungen enden nach 30 Minuten ohne Aktivität und spätestens nach 12 Stunden; Cookies nur über HTTPS, SameSite.
- Sensible Aktionen verlangen eine erneute Bestätigung (Step-up), die 15 Minuten gilt.

**Zugriff** (Berechtigungen):

- Rollen Betrachter, Operator und Admin je Node-Gruppe oder für die ganze Organisation; jede Aktion wird zentral
  geprüft, was nicht ausdrücklich erlaubt ist, wird verweigert.
- Logs, Security-Events und Befehlszeilen können Betrachtern je Organisation entzogen werden; Befehlszeilen lassen sich
  ganz ausblenden.
- Superadmins verwalten die Instanz, sehen aber ohne Mitgliedschaft keine Daten einer Organisation.
- Kiosk-Anzeigen lesen nur, nur ein Dashboard und nur, was der anlegende Admin und der Eigentümer beide sehen dürfen;
  Tokens laufen ab (1 bis 365 Tage) und sind widerrufbar.

**Trennung** der Mandanten:

- Jede Abfrage ist an eine Organisation gebunden; die Datenbank erzwingt die Trennung zusätzlich mit Row-Level
  Security. Der Server verbindet sich mit einer Rolle ohne Sonderrechte.
- Logs liegen in Loki getrennt je Organisation; Loki ist nur aus dem internen Netz erreichbar.
- Agents werden nur über ihr Client-Zertifikat einer Organisation und einem Node zugeordnet, nie über Angaben im
  Datenstrom.

**Verschlüsselung und Pseudonymisierung**:

- Web-UI nur über HTTPS (TLS 1.2+, HSTS); Agents über mTLS mit TLS 1.3 und eigener CA.
- Zugangsdaten von Kanälen und Modulen, TOTP-Seeds und der Schlüssel der Agent-CA sind mit AES-256-GCM verschlüsselt
  (Master-Key getrennt von der Datenbank); Zugangsdaten werden nach dem Speichern nie wieder angezeigt.
- Enrollment-, Einladungs-, Kiosk- und Sitzungstokens nur als Hash; Recovery-Codes nur als Hash.
- Ruhende Daten in Datenbank und Loki: [Verschlüsselung der Datenträger durch den Betreiber].

### 4.2 Integrität

- **Weitergabekontrolle:** Die UI lädt nichts von fremden Servern (keine CDNs, Schriften oder Analyse-Dienste; CSP nur
  `'self'`). Ausgehende Verbindungen zu Kanälen nur über einen geprüften Client (keine internen Adressen ohne
  Freigabe des Betreibers, keine Weiterleitung auf `http`). Keine Telemetrie, auch nicht von TimescaleDB und Loki.
- **Eingabekontrolle:** Jede Änderung steht mit Zeit und Person im Audit-Log; die Einträge sind verkettet (Hash) und
  lassen sich nicht unbemerkt ändern.
- **Datenminimierung an der Quelle:** Der Agent ersetzt Passwörter, Tokens und private Schlüssel in Logs, Befehlszeilen
  und sudo-Befehlen, bevor sie den Host verlassen; die Organisation ergänzt eigene Regeln. Was ein Host senden darf
  (Logs, Logdateien, Live-Ansicht, Ziele der Module), legt dessen Administrator lokal fest – das gewinnt immer.
- **Integrität der Software:** Agent-Binaries und Paket-Repositories sind signiert; das Installationsskript prüft die
  Signatur. Der root-Helper des Agents kennt nur feste Operationen, keine freien Befehle.

### 4.3 Verfügbarkeit und Belastbarkeit

- Agents puffern bei Ausfall des Servers (Events bis zu 7 Tage); Ingest bestätigt erst nach dem Schreiben.
- Self-Monitoring der Instanz (u. a. Datenbankgröße, verworfene Daten) mit Warnung an Superadmins.
- Container schreibgeschützt, ohne Capabilities; Datenbank und Loki ohne Route nach außen.
- Sicherungen: `sermon-server backup` sichert Stammdaten, Events, Audit-Logs und Einstellungen ohne Metriken und ohne
  Sitzungen, verschlüsselt mit age an Schlüssel des Betreibers, die nicht auf dem Server liegen; Zugangsdaten und
  TOTP-Seeds bleiben darin verschlüsselt, die Master-Keys werden getrennt gesichert. Der Restore ist geprüft (Schlüssel,
  Siegel der Datei) und wird nightly getestet (Admin-Doku „Backup und Restore“). [Zeitplan, Aufbewahrung, Ort der
  Sicherungen und der Master-Keys]. Gelöschte Daten bleiben in älteren Sicherungen bis zu deren Ablauf; ein Restore
  bringt sie zurück – Löschungen seit der Sicherung danach wiederholen.
- Wiederherstellung: [regelmäßig geprüft am …].

### 4.4 Überprüfung, Bewertung und Evaluierung

- Entwicklung: automatisierte Tests der Mandantentrennung und der Rechte (jede API-Funktion mit jeder Rolle),
  Security-Scans der Abhängigkeiten und des Codes in CI, Fuzzing der Parser, Threat Model
  (`docs/security/threat-model.md`).
- Betrieb: [Updates einspielen innerhalb von …; Review der Rollen und Mitglieder alle …; Prüfung des Audit-Logs …].

## 5. Löschfristen und Löschen

| Daten | Frist | Einstellbar |
|---|---|---|
| Logs | 1–14 Tage, ab Werk 14; Loki löscht spätestens nach 14 Tagen, die Anzahlen der Zeilen gehen mit ihnen | je Organisation (Admins); Obergrenze durch den Superadmin |
| Security-Events und Verlauf der Alerts | 7–365 Tage, ab Werk 90 | Vorgabe der Instanz (`SERMON_EVENT_RETENTION`, Betreiber); je Organisation (Admins); Obergrenze durch den Superadmin |
| Metriken | 10-s-Werte 24 h, Minutenwerte 30 Tage, Stundenwerte 1 Jahr | nein |
| Anomalie-Werte | 30 Tage | nein |
| Bekannte Anmeldequellen | 1 Jahr nach der letzten Anmeldung | nein |
| Zustellprotokoll der Benachrichtigungen | 30 Tage | nein |
| Update-Läufe | 1 Jahr | nein |
| Sitzungen | 30 Minuten ohne Aktivität, höchstens 12 Stunden | nein |
| Einladungen | 72 Stunden gültig, danach mit ihrer Adresse gelöscht (stündlich) | nein |
| Code einer neuen E-Mail-Adresse | 1 Stunde, höchstens 5 Versuche | nein |
| Audit-Logs (Organisationen und Instanz) | 2 Jahre, ab Werk 730 Tage | Betreiber: `SERMON_AUDIT_RETENTION`, 90 Tage bis 10 Jahre |
| Konten ohne Mitgliedschaft | ab Werk 30 Tage nach dem Ende der letzten Mitgliedschaft (Superadmins ausgenommen) | Betreiber: `SERMON_ACCOUNT_DELETION_GRACE`, mindestens 24 Stunden |
| Container-Logs des Servers | [Rotation des Betreibers; `deploy/compose`: 5 × 10 MB je Container] | Betreiber |

Events löscht der Server stündlich; Logs löscht Loki etwa einmal am Tag – Logs können also bis zu einem Tag länger
sichtbar sein als eingestellt.

**Node löschen** (Admins der Gruppe): Der Node wird sofort deaktiviert und nach einer Frist (ab Werk 7 Tage,
mindestens 24 Stunden, `SERMON_NODE_DELETION_GRACE`) mit Metriken, Anomalien, Security-Events, Alerts und Logs gelöscht.
Es bleiben die Einträge im Audit-Log, das Zustellprotokoll bis zu seiner Frist und Silences, die den Node nennen.

**Organisation löschen** (Superadmin): Die Organisation wird gesperrt und nach einer Frist (ab Werk 30 Tage,
mindestens 7 Tage, `SERMON_ORG_DELETION_GRACE`) mit Logs, Metriken, Events, Audit-Log, Nodes und allen übrigen Daten
gelöscht; Konten ohne Mitgliedschaft in einer anderen Organisation gehen mit. Vorher lässt sich das Audit-Log der
gesperrten Organisation exportieren.

**Konto löschen:** Ein Konto ohne Mitgliedschaft löscht der Server nach der Frist von selbst (stündlich), auf Antrag
löscht es ein Superadmin sofort (Organisationen → „Konto auf Antrag löschen“, mit erneuter Bestätigung). Mit dem
Konto gehen Sitzungen, Passwort, zweiter Faktor, Recovery-Codes, Passkeys, SSO-Verknüpfungen, eine offene Änderung der
Adresse und private Dashboards. Im Audit-Log bleibt nur die Kennung (UUID) des Kontos; jede Organisation des Kontos und
das Instanz-Audit-Log erhalten einen Eintrag ohne Adresse. Superadmins und der letzte Admin für alle Gruppen einer
Organisation lassen sich so nicht löschen.

**Audit-Logs** verlieren Einträge älter als die Aufbewahrung vom Anfang der Kette her; ein Eintrag `audit.prune` nennt
den letzten gelöschten Eintrag, damit die Kette ab dort prüfbar bleibt. Der neueste Eintrag bleibt immer.

## 6. Auftragsverarbeitung bei Mandanten (Art. 28 DSGVO)

Betreibt ihr SerMon für andere (Abschnitt 1), braucht es je Organisation einen Vertrag zur Auftragsverarbeitung. Die
folgenden Punkte zeigen, was SerMon dafür bereitstellt; den Vertrag selbst ersetzen sie nicht.

| Pflicht | Umsetzung mit SerMon | Beim Betreiber zu regeln |
|---|---|---|
| Verarbeitung nur auf Weisung | Was erhoben, gesendet und wie lange es behalten wird, stellt die Organisation selbst ein (Log-Einstellungen, Redaction, Module, Aufbewahrung, Kanäle) | Weisungen außerhalb der UI dokumentieren |
| Vertraulichkeit | Superadmins sehen ohne Mitgliedschaft keine Daten; Zugriffe der Mitglieder stehen im Audit-Log | Verpflichtung des Personals |
| TOMs | Abschnitt 4 | Betrieb, Zutritt, Sicherungen |
| Unterauftragsverarbeiter | SerMon selbst ruft keine fremden Dienste auf, außer: Mailserver, Identity Provider und die Kanäle, die die Organisation selbst einrichtet | Liste: [Hosting, Mailserver, …] |
| Unterstützung bei Betroffenenrechten | Abschnitt 7 | Ansprechpartner, Fristen |
| Meldung von Verletzungen | Audit-Log, Instanz-Audit-Log, Self-Monitoring | Meldeweg [innerhalb von … Stunden] |
| Löschen oder Rückgabe am Ende | Löschen der Organisation mit Frist; Export des Audit-Logs, Export der Security-Events als CSV, Dashboards als JSON, Regeln als YAML | Rückgabe weiterer Daten (z. B. Metriken) nach Absprache |
| Nachweise und Kontrollen | Audit-Log, diese Dokumentation, Threat Model | Kontrollrechte |
| Kontingente | Grenzen je Organisation für Nodes, Mitglieder und Aufbewahrung | – |

## 7. Rechte der Betroffenen

- **Auskunft (Art. 15):** Mitglieder laden unter **Konto → Meine Daten** alle Daten ihres Kontos als JSON herunter
  (Konto, Anmeldewege ohne Geheimnisse, Sitzungen, Mitgliedschaften und Rollen, eigene Dashboards, eigene
  Audit-Einträge; nichts über andere; mit erneuter Bestätigung, im Instanz-Audit-Log vermerkt). Security-Events lassen
  sich nach Benutzername und IP-Adresse filtern und als CSV exportieren; Logs lassen sich nach Text durchsuchen.
- **Berichtigung (Art. 16):** Mitglieder ändern Anzeigenamen und E-Mail-Adresse selbst (Konto, mit erneuter
  Bestätigung; eine neue Adresse gilt erst mit dem Code, der an sie geht, die alte Adresse wird benachrichtigt).
  Metriken, Logs und Events geben wieder, was auf den Hosts geschah, und werden nicht berichtigt.
- **Löschung (Art. 17):** Konten auf Antrag durch einen Superadmin, sonst über die Fristen (Abschnitt 5), das Löschen
  eines Nodes oder der Organisation. Einzelne Log-Zeilen oder Events lassen sich nicht gezielt löschen; eine kürzere
  Aufbewahrung wirkt auf alle.
- **Einschränkung und Widerspruch (Art. 18, 21):** Logs je Gruppe abschalten oder filtern, eigene Redaction-Regeln für
  bestimmte Angaben, Logs und Befehlszeilen für Betrachter sperren.
- **Datenübertragbarkeit (Art. 20):** für Mitglieder in der Regel nicht einschlägig; Exporte siehe Abschnitt 6.

## 8. Bekannte Grenzen

Damit die Dokumentation stimmt, sind diese Punkte im eigenen Konzept zu berücksichtigen (Stand v1.0):

- **Audit-Einträge aus Versionen vor 1.0** können eingeladene E-Mail-Adressen (`member.invite`) und die Adresse des ersten Admins
  einer Organisation (`org.create`, Instanz-Audit-Log) enthalten; sie verschwinden erst mit der Aufbewahrung der
  Audit-Logs, nicht mit dem Konto. Die Kette lässt sich nicht nachträglich schwärzen, ohne sie zu brechen.
- **Mitglieder entfernen** beendet die Mitgliedschaft sofort, das Konto aber erst nach der Frist (ab Werk 30 Tage):
  So bleibt ein Konto bei einem Wechsel der Organisation erhalten. Auf Antrag löscht ein Superadmin es sofort.
- **IP-Adressen** werden nicht nach einer Frist gekürzt oder pseudonymisiert; sie verschwinden mit ihren Events.
- **Benachrichtigungen** enthalten bei Sicherheitsregeln Benutzernamen und IP-Adressen und gehen an die Dienste der
  eingerichteten Kanäle; deren Speicherfristen bestimmt der jeweilige Dienst.
- **Logs** enthalten, was die Programme auf den Hosts schreiben. Die Redaction erkennt bekannte Geheimnisse, aber keine
  personenbezogenen Daten allgemein; dafür sind eigene Regeln und Filter je Gruppe da.
