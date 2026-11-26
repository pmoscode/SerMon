# 0039 – Modul-Framework: Laufzeit, Konfiguration, Veto und Zugangsdaten

- Status: accepted
- Datum: 2026-11-26
- Bezug: KONZEPT.md §15, D18, §4.1 (Budget), §18; ADR 0030 (Konfiguration vom Server), ADR 0026 (Zustand der
  Collectors), ADR 0035 (Helper); Threat Model TB7 (neu), T47; IMPLEMENTIERUNGSPLAN M5-01 bis M5-07

## Kontext

KONZEPT §15 legt fest: Module sind in den Agent kompiliert, ab Werk aus, werden zentral je Node-Gruppe aktiviert und
über ein Formular aus einem JSON-Schema konfiguriert; der Agent schlägt Module vor, aktiviert aber nichts; Panics und
Hänger bleiben im Modul; Zugangsdaten (etwa ein DB-Passwort) gehen „Ende-zu-Ende verschlüsselt“ an den Agent und liegen
dort nur im Speicher.

Offen war, wie das zusammenpasst mit dem, was der Agent heute vom Server annimmt (ADR 0030: eng, typisiert, lokales
Veto gewinnt). Module verschieben die Grenze: Erstmals verbindet sich der Agent auf Wunsch des Servers mit anderen
Diensten. Ein kompromittierter Server könnte jeden Agent zur Sonde im Netz seines Hosts machen – interne Dienste
abfragen, Ports abtasten, Metadaten-Dienste der Cloud lesen (T47). Das ist die neue Vertrauensgrenze **TB7: Agent ↔
Dienste des Hosts**.

Kräfte: Security (die lokale Seite muss gewinnen), Budget (< 1 % eines Kerns und < 30 MiB mit Modulen), Einfachheit
(ein Vertrag für zehn Module), Bedienbarkeit (das Formular kommt aus dem Schema, nicht aus Handarbeit pro Modul).

## Optionen

### Laufzeit

1. **Ein Task je Modul im async-Thread des Agents** mit `tokio::time::timeout` je Lauf und `JoinHandle` gegen Panics
   (der Release-Build hat `panic = "unwind"`). Blockierendes (Dateien) über `spawn_blocking`.
2. **Ein Prozess je Modul** – echte Isolation auch gegen Speicher und Endlosschleifen, aber ein Prozess kostet je
   1–3 MiB RSS und Startzeit; bei zehn Modulen sprengt das das Budget.

### Konfiguration

1. **In `AgentConfig` von ADR 0030**, je Modul an/aus und eine JSON-Konfiguration, die das Schema des Agents prüft.
2. **Eigene typisierte Proto-Nachricht je Modul** – jeder neue Parameter wäre eine Proto-Änderung in drei Sprachen, das
   Formular dennoch Handarbeit.

### Zugangsdaten

1. **Im Server versiegelt (`secrets.Box`), über den mTLS-Stream in einer eigenen Nachricht, beim Agent nur im
   Speicher.** Der Agent-Port ist direkt veröffentlicht (TB1b): Die TLS-Verbindung endet in Server und Agent, kein Proxy
   dazwischen – das ist Ende-zu-Ende zwischen Server und Agent.
2. **Zusätzlich HPKE an einen Schlüssel des Agents.** Schützt nur gegen ein Mitschneiden des Streams *im* Server
   (der Server hält den Klartext zum Versiegeln ohnehin). `ring` bietet X25519 nur für einen einzigen Schlüsselaustausch
   je Schlüssel; es bräuchte eine neue Krypto-Abhängigkeit.
3. **Im Browser an die Nodes verschlüsselt, der Server sieht nie Klartext.** Echte E2E gegen den Server ist so nicht
   erreichbar: Der Server liefert das JavaScript der UI aus, ein kompromittierter Server liefert eben anderes.
   Zudem bräuchte jeder neue Node der Gruppe eine erneute Eingabe.

## Entscheidung

### Vertrag

Trait `Module` in `sermon-modules` (Skill `sermon-add-module`): `id`, `schema` (JSON, eingebettet), `validate`,
`discover`, `collect`. Jedes Modul hinter einem Cargo-Feature, alle im Release-Build. Metriken heißen
`sermon_<modul>_<name>_<einheit>`; jedes Modul legt die Labels und ihre Höchstzahl fest (wie die Collectors).

### Laufzeit (Option 1)

- Ein Task je aktivem Modul; Intervall je Modul aus der Konfiguration, **mindestens 10 s**, Standard 30 s.
- Je Lauf ein **Zeitbudget** (Standard 5 s, höchstens 30 s) mit Abbruch; ein Lauf, der in `spawn_blocking` hängt, lässt
  sich nicht abbrechen – das Modul gilt dann als hängend und startet keinen weiteren Lauf, bis der Thread zurückkommt
  (kein Stau von Threads).
- **Größe:** höchstens 256 Samples je Lauf, jede Antwort eines Dienstes höchstens 1 MiB; mehr ist ein Fehler des Laufs.
- Ein **Panic** endet im `JoinHandle`: Das Modul wird als abgestürzt gemeldet und nach einer Pause neu gestartet (30 s,
  verdoppelt bis 30 min). Weder der Agent noch andere Module merken etwas.
- **Gesundheit** je Modul als eigener Batch-Inhalt `ModuleHealth`: `ok`, `error`, `timeout`, `hung`, `crashed`,
  `invalid_config`, `vetoed`, mit Grund (höchstens 256 Byte, durch die Redaction der Org, ohne Zugangsdaten) und dem
  letzten Erfolg. Gesendet bei Änderung und alle 10 min.
- Kein Modul startet Programme (`sh`, `curl`, `psql` …). Ausnahme nur über eine feste Helper-Operation mit eigenem
  ADR-Nachtrag (SMART, M5-18).

### Konfiguration (Option 1)

- `AgentConfig` bekommt `repeated ModuleConfig modules` (höchstens 32): `id`, `enabled`, `interval`, `config_json`
  (höchstens 16 KiB, ohne Geheimnisse). Je Node gilt die Konfiguration seiner Gruppe.
- **Version:** derselbe Zähler der Org wie ADR 0030; jede Änderung an Modulen erhöht ihn in derselben Transaktion. Wer
  später das Verschieben eines Nodes in eine andere Gruppe baut, muss ihn ebenfalls erhöhen (heute gibt es das nicht).
- **Prüfung je Modul, nicht je Konfiguration:** Ist ein Modul ungültig (Schema, Ziel außerhalb des Vetos), läuft nur
  dieses nicht (`invalid_config` mit Grund); Logs und andere Module übernimmt der Agent trotzdem. Sonst blockierte ein
  Tippfehler im nginx-Port die Log-Einstellungen der Gruppe.
- **Schema:** liegt beim Agent (`agent/crates/sermon-modules/schemas/<id>.json`); `just gen` kopiert es in den Server,
  wie die Standardregeln. Server und UI prüfen vor, maßgeblich ist der Agent. Erlaubt ist eine Teilmenge von JSON Schema
  2020-12 – ein Test prüft jedes Schema dagegen:
  - Wurzel `type: object` mit `properties`, `required`, `additionalProperties: false` (Pflicht)
  - Werte `string` (Pflicht `maxLength`; optional `minLength`, `pattern`, `enum`, `format` aus `uri`, `host-port`,
    `path`, `duration`), `integer` und `number` (Pflicht `minimum` und `maximum`), `boolean`, `array` von `string`
    oder `integer` (Pflicht `maxItems`)
  - `title`, `description`, `default`; `writeOnly: true` markiert ein Geheimnis
  - Beschriftungen der UI kommen aus den i18n-Texten (`module_<id>_<feld>`), `title` ist der Rückfall.

### Lokales Veto

`agent.toml` bekommt im Abschnitt `[local]` (wie `allow_logs`):

| Schlüssel | ab Werk | Bedeutung |
|---|---|---|
| `allow_modules` | `true` | `false`: kein Modul läuft, gemeldet als `vetoed` |
| `module_targets` | `["127.0.0.0/8", "::1/128"]` | Netze, mit denen sich Module verbinden dürfen |
| `module_hosts` | `["localhost"]` | Namen, die Module auflösen dürfen (`*.name`, `*` für alle; M5-33) |
| `module_ports` | `[80, 443, 465, 636, 993, 995, 3306, 5432, 6379, 8080, 8443, "9090-9999"]` | TCP-Ports, mit denen sich Module verbinden dürfen (`"1-65535"` für alle; M5-33) |
| `module_sockets` | `["/run", "/var/run"]` | Präfixe erlaubter Unix-Sockets |
| `module_file_prefixes` | `["/etc/ssl", "/etc/pki", "/etc/letsencrypt"]` | Präfixe lesbarer Dateien (Zertifikate) |
| `allow_docker_socket` | `false` | Sockets von Container-Runtimes sind root-äquivalent (O4), eigens freizugeben: Docker, Podman, containerd, CRI-O, BuildKit, libvirt, LXD/Incus (nach Name oder Verzeichnis) |

- Ein Hostname wird einmal aufgelöst; **jede** Adresse muss erlaubt sein, verbunden wird mit der geprüften Adresse
  (kein DNS-Rebinding zwischen Prüfung und Verbindung). Weiterleitungen folgen nur innerhalb der erlaubten Ziele,
  höchstens drei.
- Pfade wie bei den Log-Globs (ADR 0030, M3-05): segmentweise, Symlinks aufgelöst und erneut geprüft; immer gesperrt
  bleiben State-Verzeichnis, `/etc/sermon`, `/proc`, `/sys`, `/dev` und `/run/sermon` (Socket des Root-Helpers).
- Der Server erfährt das Veto (`vetoed` bzw. `invalid_config: Ziel nicht erlaubt`) und die UI zeigt es.

### Discovery

- Quellen, die der Agent ohnehin liest: lauschende TCP-Ports (`/proc/net/tcp`, `tcp6`), Prozessnamen, aktive Units.
  Ports lassen sich ohne Rechte keinem Prozess zuordnen; ein Hinweis nennt deshalb die Anlässe einzeln.
- Hinweis = Modul-ID, Anlässe (etwa `process:nginx`, `port:80`, `unit:nginx.service`) und optional Vorgaben für das
  Formular (Port); keine Rohdaten, höchstens 32 Hinweise. Geprüft alle 10 min, gesendet bei Änderung (`DiscoveryHints`).
- Der Agent aktiviert nichts; die UI fragt.

### Zugangsdaten (Option 1)

- Im Server mit `secrets.Box` versiegelt, gebunden an Org, Gruppe, Modul und Feld (AAD). Die UI bekommt nie einen Wert
  zurück, nur „gesetzt“ oder „nicht gesetzt“; das Audit-Log nennt das Feld, nie den Wert.
- Zum Agent in einer **eigenen Nachricht `ModuleSecrets`** (nicht in `AgentConfig`): nach jedem Aufbau der Session und
  bei Änderung. `Debug` der Nachricht ist in Rust geschwärzt; protobuf-go beachtet `debug_redact` nicht (Nachtrag
  M5-05 und M5-33).
- Der Agent speichert die Konfiguration ohne Geheimnisse (ADR 0030); Geheimnisse hält er nur im Speicher, nach einem
  Neustart kommen sie mit der nächsten Session. Ins WAL und in Logs gelangen sie nicht (Tests lesen die Dateien).
- „Ende-zu-Ende“ in KONZEPT §15 heißt damit: zwischen Server und Agent ohne Stelle dazwischen, die entschlüsselt. Gegen
  einen kompromittierten Server schützt keine der Optionen; die UI rät zu Monitoring-Usern mit wenigen Rechten.

## Konsequenzen

- Positiv: ein Vertrag, ein Formular-Generator und eine Prüfung für alle Module; neue Parameter ohne Proto-Änderung;
  die lokale Seite begrenzt, wohin Module dürfen – ab Werk nur Loopback.
- Negativ / Risiken:
  - Ein Modul mit Endlosschleife ohne `.await` hält den async-Thread an (Option 1 isoliert nur Panics und Wartezeiten).
    Gegenmaßnahme: Parser laufen auf begrenzten Eingaben, sind gefuzzt, und Schleifen über fremde Daten sind begrenzt;
    die Review jedes Moduls prüft das.
  - Speicher eines Moduls ist nicht vom Agent getrennt; die Größengrenzen und das Budget in der Demo (M5-31) halten ihn.
  - Wer eine nicht-lokale Datenbank überwachen will, muss `module_targets` auf dem Host erweitern – gewollt.
- Security-Auswirkungen: neue Grenze TB7 im Threat Model (T107 bis T111); Zugangsdaten der Module sind Teil von A10.
  Der Docker-Socket bleibt eigens freizugeben (O4).
- Folgearbeiten: Protokoll (M5-02), Laufzeit und Veto (M5-03), Discovery (M5-04), Zugangsdaten (M5-05), Server
  (M5-06), UI (M5-07).

## Nachtrag M5-04 (Discovery im Agent)

- **Quellen:** `/proc/net/tcp` und `tcp6` (nur `LISTEN`), `/proc/net/unix` (nur lauschende Sockets mit Pfad),
  `/proc/<pid>/comm` und die laufenden Dienste aus `systemctl list-units --type=service --state=running` – dasselbe
  `systemctl` mit festen Argumenten, leerer Umgebung und Zeitgrenze wie bei den fehlgeschlagenen Units (M2-22); kein
  Modul startet dafür etwas. Je Art höchstens 1 024 Einträge, höchstens 32 768 Prozesse. Ohne systemd (Alpine) gibt es
  keine Units; schlägt `systemctl` fehl, gelten die Units des letzten Laufs.
- **Unix-Sockets** sind kein Anlass (das Protokoll kennt nur `process:`, `port:`, `unit:`); ein Modul nutzt sie für die
  Vorgabe im Formular (etwa den Pfad eines Sockets).
- **Vertrag:** `Module::discover(&HostFacts) -> Option<Discovered>` (ab Werk `None`). `Discovered::evidence` setzt einen
  Prozess oder eine Unit voraus – ein Port allein ist irgendein Dienst – und nimmt lauschende Ports als weitere Anlässe.
  Ein Panic in `discover` kostet nur den Hinweis dieses Moduls. Anlässe außerhalb des Zeichensatzes des Protokolls
  fallen weg, höchstens acht je Hinweis; eine Vorgabe, die kein JSON-Objekt ist oder 1 KiB übersteigt, entfällt.
- **Senden:** einmal nach dem Start (der Server kennt die letzte Liste nicht), danach nur bei Änderung, auch eine leere
  Liste; gepuffert im WAL wie die Gesundheit (`Outbox::push_discovery_hints`). Mit `allow_modules = false` läuft keine
  Discovery.

## Nachtrag M5-08 (HTTP-Client und Checks)

- **Ein Client für alle Module** (`sermon_modules::http::Client`): HTTP/1.1 von hyper (über tonic schon im Baum),
  rustls mit ring; keine neue Crate. Nur `GET`, eine Anfrage je Verbindung, kein `Accept-Encoding` (nichts zu
  entpacken). Grenzen: ein Timeout für die Anfrage mit allen Weiterleitungen, 1 MiB Body, 64 KiB und 64 Header.
- **Weiterleitungen** höchstens drei, jede über `Veto::resolve`; nie von HTTPS zu HTTP; `Authorization` nur an
  Schema, Host und Port der Konfiguration. Ein Ziel außerhalb des Vetos ist eine Weigerung (`vetoed`), keine
  ausgefallene Gegenstelle.
- **TLS:** CAs des Systems oder eine CA-Datei unter `module_file_prefixes`; ohne Prüfung nur ausdrücklich
  (`tls_verify: false`), die Signaturen des Handshakes prüft rustls dennoch.
- **Zeit:** `Run::now()` liefert die Uhr der Laufzeit (`Clock`), etwa für die Restlaufzeit eines Zertifikats.
- **IDs:** Ein Modul heißt nicht wie ein Präfix der Collectors (`tcp`, `systemd`, `system`, `network` …), sonst
  mischten sich die Metriken; daher `http_check` und `tcp_check`.
- **Features:** jedes Modul hinter einem Cargo-Feature seines Namens, alle in `default`; der Client hinter `http`.

## Nachtrag M5-06: Module im Server

- **Einstellungen je Gruppe** in `module_settings` (RLS): an/aus, Intervall in ganzen Sekunden (10 s bis 1 h), die
  Konfiguration als geprüftes JSON ohne Zugangsdaten und ohne Vorgaben (die setzt der Agent aus seinem Schema ein,
  ein neuerer Agent darf andere haben). Höchstens 32 Module je Gruppe. Ändern nur Admins für alle Gruppen
  (`manage_modules`) mit Step-up; jede Änderung erhöht den Zähler der Org in derselben Transaktion und steht im
  Audit-Log (Modul, an/aus, Intervall – nicht die Konfiguration, sie kann interne Namen nennen).
- **Prüfung im Server** mit derselben Teilmenge wie der Agent (`server/internal/domain/module`). Beide lesen dieselben
  Testfälle (`agent/crates/sermon-modules/testdata/schema-vectors.json`) und melden dieselben Gründe, so sieht ein
  Admin denselben Text wie später der Agent. Ein Modul, das der Server nicht kennt, ist `NOT_FOUND`.
- **Je Node nur, was sein Agent eingebaut hat:** Der Server speichert `Hello.modules` am Node und schickt nur diese
  Module. Hat ein Agent nach einem Update andere Module, bekommt er seine Konfiguration erneut, auch in derselben
  Version: Der Server hatte weggelassen, was der alte Agent nicht ausführen konnte. Das Formular der Gruppe bietet die
  Module an, die ihre Nodes eingebaut haben (das Testmodul eines Release-Agents gibt es also nicht).
- **Gesundheit und Hinweise:** Je Node gilt der neueste Bericht und ersetzt den vorigen. Die Ingest schreibt ihn erst
  beim nächsten Flush (alle 2 s), der neueste gewinnt: Ein Agent kann den Server nicht bei jedem Batch schreiben lassen.
  Einträge unbekannter Module fallen weg, Gründe laufen durch `textsafe` und bleiben in 256 Byte, ein Vorschlag für das
  Formular wird gegen das Schema geprüft (ohne Pflichtfelder) und sonst verworfen. Lesen dürfen Viewer der Gruppe des
  Nodes; die offenen Hinweise der Fleet nennen nur Module, die der Agent eingebaut hat und die Gruppe nicht betreibt.
- **Katalog der Metriken:** Jedes Modul nennt seine Metriken in `agent/crates/sermon-modules/metrics/<id>.json` (Name
  `sermon_<id>_…`, Einheit, Aggregation, Labels); `just gen` kopiert die Datei in den Server, der sie in den Katalog
  aufnimmt (Bereich `module`). So bringt ein neues Modul seine Metriken mit, ohne dass jemand den Katalog im Server
  ändert; ein Test im Agent prüft, dass die Samples eines Moduls in seiner Datei stehen.

## Nachtrag M5-10 (systemd)

- „Kein Modul startet Programme“ bleibt: Den Zustand von Units bekommt ein Modul über den Port `Systemd` in
  `Setup.host`, den der Agent mit demselben `systemctl` wie der Collector der fehlgeschlagenen Units erfüllt (M2-22:
  feste Argumente, leere Umgebung, Zeitgrenze, begrenzte Ausgabe). Erlaubt ist nur `systemctl show` mit festen
  Eigenschaften (`Id`, `LoadState`, `ActiveState`, `SubState`, `NRestarts`) für höchstens 32 geprüfte Unit-Namen nach
  `--`. Ein eigener D-Bus-Client wäre eine schwere neue Abhängigkeit für dieselbe Auskunft.

## Nachtrag M5-19 (Prometheus-Scraper)

- Der Katalog bleibt geschlossen (KONZEPT §8): Fremde Metriken werden **keine eigenen Namen**. Jede Serie landet in
  `sermon_prometheus_gauge` oder `sermon_prometheus_rate` (Counter je Sekunde) mit den Labels `target` (Endpunkt),
  `name` (fremder Name) und `series` (fremde Labels, sortiert und escaped, höchstens 256 Byte). Verworfen: Namen
  `sermon_prometheus_<fremd>` – sie brauchten einen offenen Katalog im Server, eine Liste der Namen je Node für die
  UI und machten Einheiten zur Vermutung.
- Grenzen: höchstens 8 Endpunkte, je Endpunkt `max_series` (ab Werk 50), zusammen unter den 256 Samples eines Laufs;
  ausgelassene Serien zählt `sermon_prometheus_dropped_series`.

## Nachtrag M5-05: Zugangsdaten

- **Mit der Einstellung gesetzt:** `SetGroupModule` nimmt neue Werte (`secrets`) und zu entfernende Felder
  (`clear_secrets`) mit; eine Transaktion, ein Step-up, die Pflichtprüfung sieht gespeicherte und neue Werte. Ein
  eigener RPC hätte ein Pflicht-Geheimnis vor der Einstellung verlangt.
- **Versiegelt** in `module_secrets` (RLS, Fremdschlüssel auf die Einstellung: weg mit ihr), AAD
  `module_secret:<org>:<gruppe>:<modul>:<feld>`. Ein Wechsel des Hauptschlüssels: Werte unter einem früheren Schlüssel
  gehen auf, solange er in der Schlüsseldatei steht; neu eingegeben werden sie mit dem aktuellen versiegelt.
- **Zustellung:** nach jedem Hello und vor jeder neuen Konfiguration – auch ein leerer Satz, der alte Werte beim Agent
  löscht –, nur für eingebaute Module der eigenen Gruppe; geöffnet erst in der Session des Agents.
- **Ausgabe:** In Rust hat `ModuleSecret` ein eigenes `Debug` ohne Wert. protobuf-go schwärzt `debug_redact` in
  `String()` nicht; der Server gibt Nachrichten der Agents deshalb nie aus (Logs nennen Modul und Feld), der Test der
  Agent-Binary liest die Logs von Server und Agent auf den Klartext.

## Nachtrag M5-33 (Review A: Veto, Laufzeit, Zugangsdaten)

- **Kein DNS außerhalb des Vetos:** Ein Name wird nur aufgelöst, wenn `module_hosts` ihn erlaubt (ab Werk nur
  `localhost`); Adressen prüft der Agent ohne DNS-Anfrage. Sonst bekäme ein kompromittierter Server über die
  Gesundheitsgründe heraus, welche internen Namen es gibt und wohin sie zeigen. Eine Weigerung nennt deshalb keine
  aufgelöste Adresse und keinen Pfad, auf den ein Link zeigt, und klingt bei einem Namen, den es nicht gibt, genauso wie
  bei einem, der außerhalb von `module_targets` liegt. Wer andere Namen braucht, trägt sie ein (`*.svc.example.org`).
- **Ports:** `module_ports` begrenzt die TCP-Ports, ab Werk auf die Dienste der Module (Web, Datenbanken, TLS-Dienste,
  Exporter `9090–9999`). Loopback ist nicht harmlos: Dort lauschen oft Dienste ohne Anmeldung, manche handeln schon bei
  einem `GET` (Jolokia `8778`, Solr `8983`, Consul `8500`, etcd `2379`, das Kubelet `10255`, Docker `2375`). Ein Server
  kann ab Werk weder alle Ports von Loopback abtasten noch diese Dienste erreichen. Verworfen: `http_check` und
  `tcp_check` lokal abschaltbar zu machen – das träfe nur zwei Module, der Scraper und die Statusseiten nehmen ebenfalls
  freie URLs. Die Statusseiten von nginx und Apache nehmen keine Query (Apache nur `?auto`).
- **Restrisiko:** Auf erlaubten Ports wählt der Server weiter Pfad und Query (`http_check`, `prometheus`); ein Dienst auf
  Port 80 oder 8080, der schon bei einem `GET` etwas ändert, bleibt erreichbar. Zurück kommen nur Status, Dauer und die
  geparsten Metriken. Wer das nicht will, verkleinert `module_ports` oder setzt `allow_modules = false`.
- **Pfade:** Der Pfad, wie er konfiguriert ist, muss unter einem Präfix liegen, bevor der Agent die Platte anfasst;
  danach wird er aufgelöst und erneut geprüft. Dateien öffnet der Agent mit `O_NOFOLLOW` und `O_NONBLOCK`, nur reguläre
  Dateien, und prüft die geöffnete Datei noch einmal (wohin der Deskriptor zeigt, `/proc/self/fd`): Ein Link, der
  zwischen Prüfung und Öffnen eingesetzt wird, fällt auf. Sockets prüft er vor jeder Verbindung (auch die der
  Datenbanken) und lehnt sie in Verzeichnissen ab, in die jeder schreiben darf (`/run/lock`, `/tmp`), ebenso Links, die
  ins Leere zeigen. Gesperrt bleiben zusätzlich das konfigurierte State-Verzeichnis und das Verzeichnis der
  `agent.toml`, wie geschrieben und aufgelöst.
- **Laufzeit:** Ein Panic in `Module::build` oder `Instance::timeout` gilt wie einer im Lauf als Absturz: gemeldet und
  nach der Pause neu gebaut.
- **Werte der Zugangsdaten** prüft auch der Agent gegen ihr Feld im Schema (Länge, Steuerzeichen, Muster, wie
  `CheckSecret` im Server); ein Wert außerhalb hält das Modul mit `invalid_config` an, der Grund nennt ihn nie.
- **Zugangsdaten je Gruppe:** Der Server schickt nur die Zugangsdaten eingebauter Module, die in der Gruppe an sind;
  wer ein Modul abschaltet, nimmt seine Zugangsdaten bei der nächsten Version von den Agents. Gewollt bleibt: Jeder
  Node einer Gruppe bekommt alle Zugangsdaten ihrer laufenden Module, auch wenn der Dienst nur auf einem Node läuft –
  ein kompromittierter Node kennt sie also. Wer das trennen will, legt Gruppen je Satz von Zugangsdaten an.
- **Wer die Konfiguration sieht:** `GetNodeModules` und `ListModuleHints` geben Konfiguration, Namen gesetzter
  Zugangsdaten und Vorschläge der Discovery nur mit `manage_modules` heraus (sie nennen interne Hosts, User und Pfade);
  Viewer sehen Modul, an/aus, Intervall, Gesundheit und die Anlässe eines Hinweises.
- **Logs des Servers:** Ein `ReplaceAttr` der Handler (`internal/platform/logredact`) gibt jede Protobuf-Nachricht als
  Text ohne die Felder mit `debug_redact` aus, auch verschachtelt; die Regel „Nachrichten der Agents nie loggen“ hängt
  damit nicht mehr allein an der Disziplin.
