# 0019 – Alerting: Kanäle, statistische Anomalien, Regeln in UI und YAML

- Status: accepted
- Datum: 2026-11-20 (Entscheidung aus der Konzeptrunde, nachträglich dokumentiert)
- Bezug: KONZEPT.md §2 (D14, D16, D22), §12, §13, §18.6

## Kontext

Alerts sind der Teil von SerMon, der Menschen weckt. Sie müssen dort ankommen, wo das Team arbeitet, dürfen nicht
flattern und müssen nachvollziehbar sein. Feste Schwellen passen nicht zu jedem Server; Machine-Learning-Modelle
sind schwer zu erklären und zu testen. Regeln sollen sich versionieren lassen.

## Optionen

1. **Kanäle:** nur E-Mail und Webhook (Rest über Dritte) oder die gängigen Chat- und Push-Dienste direkt.
2. **Anomalien:** keine, ML-Modelle (Training, schwer erklärbar) oder statistische Verfahren mit Baselines.
3. **Regeln:** nur UI, nur Dateien, oder UI mit YAML-Import/-Export.

## Entscheidung

- **D14 – Kanäle direkt eingebaut:** E-Mail (SMTP mit TLS-Pflicht), Webhook (HMAC-signiert), Slack, Teams, Discord,
  Mattermost, Telegram, ntfy, Gotify, Pushover. Ein gemeinsames `Notifier`-Interface, „Test senden“, verschlüsselte
  Zugangsdaten, SSRF-Schutz für alle ausgehenden Requests.
- **D16 – Anomalien statistisch und serverseitig:** robuster Z-Score gegen saisonale Baselines (Median/MAD je Stunde
  der Woche) und EWMA; anomal nur, wenn beide zustimmen und es mehrere Minuten anhält; 7 Tage Lernphase.
- **D22 – Regeln in der UI und als YAML** (Import/Export, gleiche Validierung); Standardregeln werden mitgeliefert.

## Konsequenzen

- Positiv: Benachrichtigungen ohne Zusatzdienste; Anomalien sind erklärbar („5× höher als dienstags um diese Zeit“)
  und mit synthetischen Reihen golden-testbar; Regeln lassen sich per Git verwalten.
- Negativ: Jeder Kanal ist Code, der gepflegt werden muss; statistische Verfahren erkennen keine komplexen Muster.
- Security: Ausgehende Requests nur nach SSRF-Prüfung (keine internen Ziele ohne Freigabe); Webhook-Signaturen mit
  Zeitstempel gegen Replay.
- Folgearbeiten: Alert-Engine, Kanäle und Regeln in M2, Anomalien in M4; weitere Detektoren hinter dem Interface
  `Detector` in v2.

## Nachtrag M2-02: Ausführung der Engine

- Jede Server-Instanz versucht alle 10 s, die Orgs mit Regeln zu leasen (Compare-and-swap auf `alert_leases`, 30 s,
  in der Tenant-Transaktion der Org). Nur der Halter wertet aus; fällt er aus, übernimmt eine andere Instanz nach
  Ablauf. Das erfüllt „Leases in PG“ aus dem Plan, ohne eine Transaktion über die ganze Auswertung offen zu halten.
- Zustand und Historie liegen in PostgreSQL mit RLS; die Historie ist für die Anwendung nur anhängbar. Wieder ruhige
  Instanzen (OK ohne Flapping-Historie) werden gelöscht.


## Nachtrag M2-04: Regelformat, Standardregeln und Overrides

- **Schema aus dem Proto:** Die Regel ist die Nachricht `sermon.web.v1.AlertRule`. Sie liegt im Web-Paket, damit der
  Regel-Editor (M2-17) sie als TypeScript bekommt; der Agent braucht sie nicht. YAML wird mit `go.yaml.in/yaml/v3`
  gelesen, als JSON strikt per `protojson` übernommen und mit protovalidate geprüft. Unbekannte Felder meldet ein
  eigener Durchlauf über den YAML-Baum mit Zeile und Pfad, denn `protojson` kennt nur die Position im JSON.
- **Typprüfung über den Katalog:** Felder eines anderen Regeltyps sind Fehler, ebenso Verhältnisse außerhalb von 0..1.
  So scheitert „85“ statt „0.85“ beim Laden statt still nie auszulösen.
- **Standardregeln eingebettet:** `rules/` ist die Quelle. `just gen-rules` kopiert sie in den Server, `check-gen`
  hält die Kopie aktuell (wie bei `install.sh`). Kein Laden zur Laufzeit von der Platte.
- **Overrides als ganze Dokumente:** Pro Org und Regel speichert `alert_rule_overrides` (RLS) „abgeschaltet“ und/oder
  ein vollständiges Regeldokument; eigene Regeln sind Overrides ohne Standardregel. Feldweise Patches wurden verworfen:
  Sie brechen still, wenn sich die Standardregel ändert. Ein Override, das nicht mehr lädt, wird protokolliert und
  ignoriert, damit die Org die Regel nicht verliert.
- **Ende von Alerts:** Gilt eine Regel für eine Instanz nicht mehr, setzt die Engine einen feuernden Alert auf
  `RESOLVED` (mit Historie, damit die Notifier „behoben“ melden) und löscht die Instanz. Eine Regel, deren Auswertung
  fehlschlägt, behält ihre Alerts.
- **Last:** Fenster über 1 h lesen mit grobem Schritt (24 h → 5 min) aus dem 1-min-Aggregat statt aus den Rohdaten.
  Zustandsregeln holen den aktuellen Wert getrennt aus den letzten 5 min.

## Nachtrag M2-08: Event-Regeln

- **Lesen statt Strom:** Ingest und Auswertung einer Org laufen auf verschiedenen Instanzen (M2-07). Jede Runde liest
  deshalb die Events, die im Zählfenster **angekommen** sind (`received_at`, Index `(org_id, received_at, id)`), und
  zählt sie nach ihrer **Entstehung** (`occurred_at`): das Maximum an Events innerhalb eines Fensters (halb offen).
  Ein Cursor wurde verworfen; er bräuchte Zustand im Speicher, der bei der Übernahme des Leases verloren geht, und
  wegen der Commit-Reihenfolge einen Abstand zu `now()`. Das gleitende Fenster liest jedes Event mehrfach, ist aber
  zustandslos.
  - Ein Rückstau nach einem Ausfall des Agents löst beim Eintreffen aus, wenn er einen Burst enthält; ein Tag
    verstreuter Fehlversuche, der auf einmal ankommt, löst nicht aus.
  - Ein Alert hält, solange die Zahl im Fenster reicht, und löst sich danach. Das Fenster ersetzt `clear`.
- **Instanzen:** Regel × Node × `groupBy` (`ip`, `user`). **IPv6-Adressen zählen pro /64**, sonst entgeht ein
  Angreifer der Schwelle, indem er Adressen in seinem Netz rotiert. Für die Fleet-Korrelation (`count.of: nodes`)
  bekommt jeder beteiligte Node eine Instanz; eine Org-Instanz ohne Node hätte das Schema (Fremdschlüssel auf
  `nodes`) und die Node-Ansicht gebrochen. Die Korrelation sieht nur die Nodes der eigenen Org (RLS).
- **Offene Instanzen ohne Events** meldet die Regel als 0, dafür bekommt sie die offenen Instanzen der Engine.
- **Grenzen gegen Fluten:** höchstens 50 000 Events pro Abfrage (die neuesten), höchstens 100 Instanzen pro Regel und
  Runde (die höchsten Zahlen zuerst). Bei einer Kappung behalten offene Instanzen ihren Zustand, statt auf einer
  unvollständigen Zahl zu enden. Zähler `sermon_server_alert_event_reads_truncated_total`. Regeln derselben Art und
  desselben Fensters teilen sich eine Abfrage pro Runde.
- **Felder:** `notifyFields` kopiert Felder des neuesten Events (`ts`, `user`, `ip`, `port`, …) in die Transition und
  die Instanz, gespeichert als `fields` (jsonb, Migration 00023). Die Werte wählt ein Angreifer: Steuerzeichen werden
  zu U+FFFD, jeder Wert endet nach 256 Bytes. Dasselbe gilt für Dimensionen aus User-Namen.
- **Neue Felder im Schema:** `event`, `match` (nur gleich, z. B. `user: root`), `groupBy`, `count` (`window`,
  `warning`, `critical`, `of`), `after` (vorausgehende Events: Art, Anzahl, Fenster; gleicher Node und gleiche
  `groupBy`-Werte) und `notifyFields`. Welche Felder eine Art hat, legt `secevent.FieldsOf` fest; der Loader prüft
  dagegen.

## Nachtrag M2-10: Notifier-Framework

- **Warteschlange in PostgreSQL:** `notification_deliveries` ist Queue und Zustellprotokoll zugleich, mit RLS wie alle
  Tenant-Tabellen. Jede Server-Instanz holt fällige Zeilen pro Org mit `FOR UPDATE SKIP LOCKED` und einem Claim von
  2 min (seit M2-38 so lang wie ein Batch: 3 min, siehe Nachtrag). Fällt eine Instanz beim Senden aus, übernimmt nach Ablauf des Claims eine andere. Das ergibt
  „mindestens einmal“: Eine Nachricht kann doppelt ankommen, geht aber nicht verloren. Eine Queue im Speicher hätte
  bei einem Neustart Benachrichtigungen verloren; ein eigener Broker widerspräche dem einfachen Betrieb.
- **Retries:** höchstens 6 Versuche (nach 30 s, 1, 2, 4, 8 min), `Retry-After` bei 429 bis 1 h. Dauerhafte Fehler
  scheitern sofort: 4xx außer 408 und 429, gesperrte Ziele aus `safehttp` und gelöschte oder abgeschaltete Kanäle.
- **Aufbewahrung:** Das Protokoll enthält die Felder der Alerts (User, IP) und wird nach 30 Tagen gelöscht.
  Die API zeigt daraus nur Art, Regel und Node.
- **Zugangsdaten:** pro Kanal als JSON mit `secrets.Box` versiegelt, Kontext `notification_channel:<org>:<id>`.
  Sie sind nur schreibbar; ein Update ersetzt nur die genannten. Fehlertexte werden von den Zugangsdaten des Kanals
  bereinigt, falls ein Dienst sie zurückgibt.
- **Deep-Links** zeigen auf den ersten Eintrag von `SERMON_ALLOWED_ORIGINS`, bis M2-16 auf `/nodes/<id>`.
- **Noch nicht verbunden:** Die Engine liefert ihre Transitionen erst mit dem Routing (M2-14) an
  `Dispatcher.Enqueue`. Die Kanaltypen folgen ab M2-11; bis dahin ist die Registry leer.

## Nachtrag M2-11: E-Mail, Webhook, ntfy

- **Webhook-Signatur** `t=<Unix-Sekunden>,v1=<hex HMAC-SHA256 über "<t>.<Body>">`: Der Zeitstempel ist signiert, ein
  Empfänger lehnt alte Requests ab (Replay). Das Verfahren ist das von Stripe, für das Empfänger oft schon Code haben.
  Gesendet wird nie ein Token im Klartext.
- **ntfy:** Das Topic ist ein Geheimnis wie das Token, weil auf öffentlichen Servern jeder mitliest, der es kennt.
  Gesendet wird JSON an die Wurzel des Servers statt Header (`Title`, `Priority`). So muss kein Wert mit
  Nicht-ASCII-Zeichen in einen Header, und Markdown bleibt aus.
- **E-Mail:** eine Mail pro Empfänger statt einer mit allen in `To`. Die Empfänger sehen sich nicht, und eine
  abgelehnte Adresse hält die anderen nicht auf. Bei einem Retry kann ein Empfänger, der die Mail schon hat, sie
  noch einmal bekommen; das passt zu „mindestens einmal“. Der Mailserver ist der des Betreibers, darum läuft E-Mail
  nicht über `safehttp`.

## Nachtrag M2-12: Slack, Teams, Discord, Mattermost

- **Werte der Hosts möglichst ohne Escaping:** Wo das Format reinen Text kennt, kommen die Werte dorthin, statt sie
  zu escapen: Slack `plain_text`, Teams `TextRun`. Das hängt nicht davon ab, wie genau ein Dienst Markdown auslegt.
  Bei Teams ist die Wirkung von Backslash-Escapes in Adaptive Cards nicht verlässlich dokumentiert. Discord und
  Mattermost haben keinen reinen Textweg: Werte werden für Markdown escapt und Erwähnungen mit einem unsichtbaren
  Zeichen entschärft; Discord bekommt zusätzlich ein leeres `allowed_mentions`.
- **Webhook-URLs sind Geheimnisse:** Wer sie kennt, schreibt in den Kanal.
- **Prüfung der Hosts:** Slack und Discord nur auf ihren offiziellen Webhook-Hosts. Das fängt Kopierfehler ab; vor
  SSRF schützt ohnehin `safehttp`. Teams-Workflows und Mattermost haben keine festen Hosts.
- **Teams über Workflows:** Microsoft stellt die Office-365-Connectors ein; Workflows nehmen Adaptive Cards
  im Format `{"type": "message", "attachments": [...]}`.

## Nachtrag M2-13: Telegram, Gotify, Pushover

- **Reiner Text, wo es geht:** Gotify (`text/plain`) und Pushover (ohne `html=1`) zeigen Werte wörtlich. Telegram
  bekommt HTML, damit der Titel fett ist; Werte werden HTML-escapt und Erwähnungen entschärft. Ohne `parse_mode`
  wäre der Titel nicht hervorgehoben, und Erwähnungen in User-Namen müssten trotzdem entschärft werden.
- **Telegram-Link als Text:** Inline-Buttons lehnen Links auf `localhost` oder ohne Domain ab, und eine abgelehnte
  Nachricht wäre ein dauerhafter Fehler. Der Link steht darum als Text am Ende.
- **Telegram-Token im Pfad:** Die Bot-API kennt keinen Header für das Token. `safehttp` nennt in Fehlern nur
  Methode und Host, der Dispatcher entfernt die Zugangsdaten aus jedem Fehlertext; ein Test sichert das ab.
  `httppost.PostWith` liest die Wartezeit nach 429 aus dem Body, weil Telegram keinen `Retry-After`-Header sendet.
- **Feste Hosts:** Telegram und Pushover haben feste API-Adressen; Gotify ist selbst betrieben und nimmt jede URL
  der Outbound-Policy.
- **Keine Notfall-Priorität bei Pushover:** Priorität 2 wiederholt, bis in Pushover bestätigt wird, und braucht
  eigene Parameter. Eskalation ist Aufgabe der Policies (M2-14).
- **Thema bei Telegram:** Für Gruppen mit Themen gibt es das optionale Feld `thread` (`message_thread_id`).

## Nachtrag M2-14: Notification-Policies und Eskalation

- **Policies:** Matcher auf Severity, Gruppe des Nodes, Regel-ID (Muster mit `*`) und die Labels der Regel; jeder
  gesetzte Matcher muss passen. Alle passenden Policies greifen, jeder Kanal bekommt eine Änderung aber nur einmal
  (mit der Gruppierung der ersten Policy nach Name). Eine Policy hat 1 bis 10 Kanäle.
- **Abweichung, `tags`:** Nodes haben noch keine Tags. Stattdessen passen die Labels der Regel (z. B.
  `category: security`). Tags an Nodes kommen mit M2-29.
- **Ohne Policies an alle aktiven Kanäle:** Eine Org ohne eine einzige Policy bekommt jeden Alert auf allen aktiven
  Kanälen, gruppiert pro Node, ohne Wiederholung. Abgeschaltete Policies zählen als vorhanden. So wirkt der erste
  Kanal sofort, und wer Policies anlegt, entscheidet selbst.
- **Wiederholung und Eskalation:** Solange ein Alert feuert und nicht quittiert ist, geht er nach dem Intervall der
  Policy wieder raus (5 min bis 7 Tage). Nach der Eskalationszeit (1 min bis 24 h) bekommen ihn die Kanäle der
  Eskalation, die ihn noch nicht haben; danach hören sie alles zu diesem Alert, auch das Ende. Quittieren stoppt
  beides, bis der Alert endet oder kritisch wird. Der Zustand (letzte Nachricht, eskaliert) liegt pro Instanz und
  Policy in `alert_routing_state` und geht mit Instanz und Policy.
- **Eine Transaktion:** Der Router schreibt Zustellungen und Zustand zusammen. Eine Erinnerung wird so nie doppelt
  eingereiht, auch wenn zwei Runden schnell folgen. Fällt das Schreiben aus, holt die nächste Runde fällige
  Erinnerungen nach; eine Änderung, deren Runde ausfällt, geht verloren (wie bisher zwischen Engine und Sink).
- **Router als Sink der Engine:** Die Engine gibt jede Runde, die sie hält, mit den Instanzen danach weiter, auch
  ohne Änderung. Ohne Änderung und ohne feuernden, unquittierten Alert liest der Router nichts.
- **Flattern:** Während eine Instanz flattert, fallen Feuern und Behoben weg. Die Änderung, mit der das Flattern
  beginnt, und die Hinweise „flattert“ und „stabil“ gehen raus, Erinnerungen auch.
- **Gruppierung pro Runde:** „Pro Node“ fasst die Nachrichten einer Runde (10 s) für einen Node und Kanal zusammen,
  „Digest“ alle. Mehrere werden zu einer Sammelnachricht (`digest`): Titel und Farbe vom dringendsten Eintrag,
  höchstens 20 Einträge und 12 KiB (das Protokoll erlaubt 16 KiB, auch bei langen Regelnamen), der Rest
  gezählt. **Abweichung:** kein Sammelfenster über mehrere Runden; Alerts, die
  zusammen auftreten, fallen meist in dieselbe Runde. Einträge eines Digests haben keine Felder (User, Adresse):
  Das Protokoll bleibt so klein, die Details stehen in SerMon. Der Webhook bekommt die Einträge als Daten.
- **Rechte:** Policies verwalten Admins mit `manage_policies`, Schreiben nur mit frischem Step-up, wie bei den
  Kanälen. Eine Policy entscheidet, wer von Security-Events erfährt (T67). Kanäle und Gruppen müssen zur Org
  gehören; ein gelöschter Kanal verschwindet aus den Policies.

## Nachtrag M2-15: Silences und Wartungsfenster

- **Silences schalten Benachrichtigungen stumm, nicht Alerts:** Die Engine wertet weiter aus, Instanzen und Historie
  bleiben vollständig. Der Router hält Feuern, Quittieren, Flattern, Erinnerungen und Eskalationen zurück, solange
  eine Silence oder ein Wartungsfenster greift (`SILENCED` aus §12.1 ist damit ein Zustand des Routings).
- **Nachholen:** Feuert ein Alert nach dem Ende noch, kommt er einmal mit „Stummschaltung beendet“ und der Dauer.
  Die Eskalation zählt ab dieser Nachricht, nicht ab dem Feuern; sonst eskalierte jeder Alert, der eine Wartung
  überdauert, sofort. Endet ein Alert stumm, erfahren die Kanäle nichts davon. Kannten sie ihn schon, hören sie auch
  in der Stummschaltung, dass er behoben ist.
- **Selektor:** Matcher wie bei den Policies, dazu einzelne Nodes (nur zusammen mit ihren Gruppen). Eine Silence
  muss Gruppen, Regeln oder Labels nennen; „alles“ geht nicht aus Versehen. Höchstens 30 Tage, ein Kommentar ist
  Pflicht, höchstens 200 nicht beendete pro Org. Beendete bleiben 30 Tage gespeichert und werden einen Tag angezeigt.
- **Rechte:** `silence_alerts` (Operator) pro Gruppe. Eine Silence für alle Gruppen braucht das Recht auf allen.
  Anlegen nur mit frischem Step-up, weil eine Silence Security-Alerts verbergen kann (T69). Beenden ohne Step-up,
  weil es nur lauter macht. Mitglieder sehen die Silences ihrer Gruppen und die für alle Gruppen.
- **Wartungsfenster:** wöchentlich (Wochentage, Start, Dauer, IANA-Zeitzone), pro Gruppe oder für alle. Im Fenster
  sind nur „Node offline“ und „Reboot“ stumm. Die Form entspricht dem `window` der Update-Policies (§14.2); M4 kann
  sie dort wiederverwenden. Verwalten Admins mit `manage_policies` und Step-up. Die Zeitzonen sind in den Server
  eingebettet (`time/tzdata`), damit sie ohne die Zeitzonendaten des Hosts gelten.
- **Änderung zu M2-14:** Der Rückfall ohne Policies merkt sich seine Zustände unter der Policy-ID null, damit er
  Zurückgehaltenes nachholen kann. Deshalb verweist `alert_routing_state` nicht mehr auf `notification_policies`;
  der Router entfernt die Zustände abgeschalteter oder gelöschter Policies in der nächsten Runde.
- **Korrektur zu M2-14:** Policies verwaltet jetzt `manage_policies` statt `manage_channels` (beide Admin für die
  ganze Org, also dieselbe Wirkung).

## Nachtrag M2-16: Alert-Seite und Quittieren

- **Quittieren über die API neben der Engine:** Die Engine speichert die ganze Instanz aus ihrer Kopie vom Anfang
  der Runde. Eine Quittierung aus der API dazwischen ginge so verloren. Das Upsert behält deshalb eine gespeicherte
  Quittierung, wenn die Engine keine mitbringt, der Alert weiter feuert (gleiches `fired_at`) und nicht von Warnung
  zu kritisch eskaliert. Genau dann behielte auch die Engine sie. Die API quittiert per bedingtem Update (nur
  feuernd und noch nicht quittiert) zusammen mit dem Eintrag in der Historie.
- **Routing des Quittierens:** Die API reicht den Übergang als Teil-Runde (`Round.Partial`) an den Router. Der
  meldet nur diese Änderung; Erinnerungen, Eskalationen und Aufräumen bleiben bei den Runden der Engine, damit
  nichts doppelt eingereiht wird.
- **Step-up nur für Security-Alerts:** Quittieren stoppt Erinnerung und Eskalation. Bei Regeln `security.*` ist das
  so heikel wie eine Silence (T69); bei den anderen Alerts soll Quittieren im Incident schnell gehen.
- **Felder nur mit `view_security_events`:** User und Adresse von Security-Alerts sind personenbezogen. Liste und
  Historie zeigen sie nur, wo der Aufrufer die Security-Events der Gruppe sehen darf (auch abhängig von der
  Org-Einstellung für Viewer).
- **Aufbewahrung der Historie: 1 Jahr**, wie die Security-Events. `sermon_app` darf nur Einträge löschen, die älter
  sind; das erzwingt eine einschränkende RLS-Policy (`AS RESTRICTIVE FOR DELETE`). Die jüngere Historie bleibt für die
  App unveränderlich. Der Server löscht alle 6 Stunden.
- **Stumm-Anzeige:** Die Seite markiert Alerts, die eine aktive Silence oder ein Wartungsfenster stumm schaltet, mit
  derselben Logik wie der Router.

## Nachtrag M2-18: Kanäle, Policies und Silences in der UI

- **Seite „Benachrichtigungen“ nur für Admins:** Kanäle, Policies, Wartungsfenster und das Protokoll verwalten Admins
  der ganzen Org. Silences legen Operatoren an; sie stehen deshalb als Tab auf der Alert-Seite, mit „Stummschalten“
  an jedem Alert (Gruppe, Node und Regel vorausgefüllt).
- **Zugangsdaten nur schreibend:** Das Formular entsteht aus den Feldern des Kanaltyps. Geheime Felder sind
  Passwortfelder, die nie einen gespeicherten Wert zeigen; leer heißt „behalten“, gesendet wird nur Getipptes. Die
  Liste nennt nur, welche Zugangsdaten gesetzt sind.
- **Wo jemand stummschalten darf,** sagt die Session (`silence_group_ids`, wie `enroll_group_ids`). Die UI bietet nur
  diese Gruppen an und blendet sonst die Knöpfe aus; der Server prüft trotzdem jeden Aufruf.
- **Fehler bei ungültiger Eingabe** zeigen die Begründung des Servers. Sie nennt das Feld, nie einen Wert.

## Nachtrag M2-17: Regel-Editor, Import und Export

- **Ein Weg für Editor und Import:** `ImportAlertRules` nimmt ein oder mehrere YAML-Dokumente (getrennt durch
  `---`). Mit `dry_run` zeigt es Fehler und die Änderungen je Regel als Vorher/Nachher, ohne zu speichern; sonst
  speichert es alle Dokumente oder keines, in einer Transaktion mit einem Audit-Eintrag. Unveränderte Regeln bleiben
  unberührt, eine abgeschaltete Regel bleibt abgeschaltet.
- **Fehler an der richtigen Stelle:** Der Loader prüft jedes Dokument wie die Standardregeln (keine freien Abfragen,
  unbekannte Felder abgelehnt). Zeilennummern beziehen sich auf den ganzen Text: Jedes Dokument wird mit so vielen
  Leerzeilen davor geprüft, wie es im Text weiter unten steht.
- **Der geschriebene Text bleibt:** `alert_rule_overrides.source` hält das YAML, wie es eingegeben wurde (Kommentare,
  Reihenfolge); die Engine liest weiter nur das geprüfte JSON. Ohne Text rendert der Server das Dokument als YAML.
  Standardregeln zeigen ihre Datei.
- **Rechte:** Lesen und Exportieren dürfen alle Mitglieder, ändern nur Admins (`manage_alert_rules`) mit frischem
  Step-up, weil Abschalten auch Security-Alerts verstecken kann (T69). Eine Prüfung ohne Speichern braucht keinen
  Step-up.
- **Export** liefert die geänderten und eigenen Regeln oder alle als ein YAML. Ob eine Regel abgeschaltet ist, steht
  nicht darin; das bleibt eine Einstellung der Org.
- **UI:** Tab „Regeln“ auf der Alert-Seite statt eines weiteren Menüpunkts.

## Nachtrag M2-28: Security-Review

- **Neue Adresse, neue Zugangsdaten:** Ändert ein Update ein Feld vom Typ URL (Server von ntfy und Gotify, Webhook-URL),
  müssen alle gespeicherten Zugangsdaten mitkommen. Sonst schickte „Test senden“ das Token an einen Server des Admins.
- **Quittieren mit Step-up** hängt am Regeltyp `event` und der Kategorie `security` statt am Präfix `security.` – auch
  eigene Security-Regeln einer Org.
- **Dimension für Viewer ohne Logs:** Die Dimension von Event-Regeln nennt Adresse oder User. Ohne
  `view_security_events` kommt sie mit der `secrets.Box` versiegelt (gebunden an Org, Regel und Node, Präfix `~`); Verlauf
  und Quittieren lösen sie serverseitig auf.
- **Text vom Host in den Kanälen** ohne Format- und Trennzeichen (`textsafe`); Namen lehnen sie ab.

## Nachtrag M2-38: Fairness zwischen Organisationen

Befund des Security-Reviews M2-28 (T80): Engine und Dispatcher arbeiteten alle Orgs nacheinander ab, ohne Zeitbudget;
eine Org mit vielen eigenen Regeln oder toten Zielen verzögerte die anderen, und eine Instanz konnte nach Ablauf ihres
Leases bzw. Claims noch speichern (doppelte Benachrichtigungen bei mehreren Instanzen).

- **Engine:** Zeitbudget pro Org (`OrgBudget`, 20 s, unter dem Lease von 30 s); die Regeln lädt und parst nur noch der
  Halter des Leases. `Save` prüft den Lease in derselben Transaktion (`SELECT … FOR UPDATE` auf `alert_leases`): Hat
  ihn inzwischen eine andere Instanz, wird nichts gespeichert und nichts an das Routing gegeben (`ErrLeaseLost`).
- **Dispatcher:** Orgs laufen nebeneinander (höchstens 16), eine noch beschäftigte Org wird bei der nächsten Abfrage
  übersprungen; die Zustellungen einer Org laufen zu viert. Der Claim deckt einen Batch ab (20 Zustellungen / 4 ×
  30 s + 30 s = 3 min); `Finish` schreibt nur mit dem eigenen Claim (`ErrClaimLost`).
- **Grenzen:** höchstens 50 Kanäle und 200 eigene Regeln pro Org (geänderte Standardregeln zählen nicht) und 100 Mails
  pro Org und Stunde über den Mailserver des Betreibers. Was darüber geht, wartet die Stunde ab (zählt als Versuch;
  nach 6 Versuchen ist es gescheitert).
- **Verworfen für jetzt:** einstellbare Kontingente für Regeln, Kanäle und Mails pro Org (ADR 0029). Die festen Grenzen
  schließen die Lücke; als Kontingent brauchten sie Migration, API und UI. Sie lassen sich später zu den Kontingenten
  von M2-32 hinzufügen.
- **Nicht gecacht:** die geparsten Overrides. Seit nur noch der Halter lädt, fällt das Parsen einmal pro Org und Runde
  an.
