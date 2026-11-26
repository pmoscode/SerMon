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
| `module_sockets` | `["/run", "/var/run"]` | Präfixe erlaubter Unix-Sockets |
| `module_file_prefixes` | `["/etc/ssl", "/etc/pki", "/etc/letsencrypt"]` | Präfixe lesbarer Dateien (Zertifikate) |
| `allow_docker_socket` | `false` | der Docker-Socket ist root-äquivalent (O4), eigens freizugeben |

- Ein Hostname wird einmal aufgelöst; **jede** Adresse muss erlaubt sein, verbunden wird mit der geprüften Adresse
  (kein DNS-Rebinding zwischen Prüfung und Verbindung). Weiterleitungen folgen nur innerhalb der erlaubten Ziele,
  höchstens drei.
- Pfade wie bei den Log-Globs (ADR 0030, M3-05): segmentweise, Symlinks aufgelöst und erneut geprüft; immer gesperrt
  bleiben State-Verzeichnis, `/etc/sermon`, `/proc`, `/sys`, `/dev`.
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
  bei Änderung. `Debug` der Nachricht ist in Go und Rust geschwärzt.
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
