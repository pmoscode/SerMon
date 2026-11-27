# Lasttest und Performance-Nachweis

SerMon bringt zwei Skripte mit, um die Last einer Installation zu messen:

- [`scripts/load-report.sh`](../../../scripts/load-report.sh) **misst** eine laufende Installation in festen Abständen
  und schreibt einen Bericht in Markdown mit Grenzwerten, Bestanden/Nicht bestanden und einer Hochrechnung des
  Plattenbedarfs auf 7 Tage und die Aufbewahrung. Es braucht nur `sh`, `awk`, `curl` und – wenn vorhanden – Docker
  auf dem Server-Host; was es nicht erreicht, steht im Bericht als „n/a“.
- [`scripts/load-test.sh`](../../../scripts/load-test.sh) (`just load-test`) **erzeugt** die Last auf der
  Entwicklungsmaschine: die Produktions-Compose mit virtuellen Nodes, dann die Messung (V1-08).

Der Nachweis für den Richtwert (KONZEPT §22: 50 Nodes auf 4 vCPU, 8 GB RAM, 200 GB SSD) kommt aus dem Testbetrieb
(V1-18) mit genau diesem Mess-Skript.

## Was gemessen wird

| Teil | Quelle | Im Bericht |
|---|---|---|
| CPU und Speicher von `server`, `postgres`, `loki`, `caddy` (und z. B. dem Simulator) | `docker stats` | Mittel, p95, Maximum; CPU in % eines Kerns |
| Ingest: Samples, Batches, Logzeilen, Security-Events pro Sekunde, Lag, Verworfenes | `/metrics` des Servers ([ADR 0011](../../adr/0011-server-metriken-endpunkt.md)) | Raten, Lag p50/p95/Max, verworfene Einträge im Zeitraum |
| Abfragezeiten der UI: Flotte, Sparklines, Node, Charts über 1 h, 24 h und 30 Tage, Logs, Log-Histogramm | Web-API mit echter Session | p50, p95, Maximum, Fehler |
| Größe der Datenbank je Teil (Rohdaten, 1-min- und 1-h-Werte, Anomalien, Rest, größte Tabellen), WAL, Checkpoints, fehlgeschlagene Jobs, Security-Events | SQL | Wachstum pro Stunde (ganzer Zeitraum und zweite Hälfte) |
| Volumes von PostgreSQL (mit WAL) und Loki | `du` im Volume | Wachstum pro Stunde |

**Hochrechnung:** Jeder Teil wächst wie in der zweiten Hälfte des Zeitraums (nach dem Anlaufen) bis zu seiner
Aufbewahrung – Rohdaten 24 h, `metrics_1m` und Anomalien 30 Tage, `metrics_1h` 365 Tage, Loki 14 Tage, der Rest der
Datenbank ohne Grenze. Die Rohdaten rechnet der Bericht aus den schon komprimierten Chunks (22 h komprimiert, die
jüngsten 2 h nicht); gibt es noch keinen, nimmt er das Wachstum des Zeitraums über 24 h als obere Grenze. Der WAL
zählt mit `max_wal_size`. `--horizon TAGE` ändert den zweiten Zeitpunkt (Standard 365).

**Grenzen:** `load-report.sh limits` zeigt die Standardwerte (Richtwert mit Luft für den Host, die Kriterien des
Spikes aus [ADR 0001](../../adr/0001-zeitreihen-speicher-timescaledb.md), nichts verworfen). Eigene mit
`--limit 'SCHLÜSSEL.STATISTIK<=WERT'` oder `>=`; eine Grenze ersetzt die Standardgrenze desselben Schlüssels. Der
Bericht endet mit einer Gegenprobe (jede eingehaltene Grenze knapp unter dem Messwert muss scheitern) und dem
Ergebnis; der Exit-Code ist 1, wenn eine Grenze überschritten ist.

## Auf der Entwicklungsmaschine

```sh
just load-test 3h 50                 # Dauer und Anzahl der Nodes
SKIP_BUILD=1 just load-test 10m 50   # Builds und Secrets des letzten Laufs wiederverwenden
KEEP=1 …                             # Umgebung danach weiterlaufen lassen (https://localhost:19443)
```

Das Skript baut, was die Demo baut, dazu den Agent mit dem Simulator, und startet die Produktions-Compose mit dem
E2E-Overlay und `deploy/load/compose.yaml` als eigenes Projekt `sermon-load` (eigene Ports und eigenes Netz; eine
Demo kann daneben laufen). Es legt einen Admin, eine Gruppe `load` mit dem Modul `simulate` und ein
Enrollment-Token mit genug Nutzungen an und startet die virtuellen Nodes in einem Container mit den Szenarien
`cpu-high`, `ssh-bruteforce` und `flapping` und 2 synthetischen Journal-Zeilen pro Sekunde und Node
(`SCENARIOS`, `LOG_RATE`). Ein virtueller Node sendet etwa so viele Serien wie ein echter Host (rund 110). Bericht und Rohdaten liegen danach unter `deploy/load/.runs/<Zeit>/`. Für lange Läufe auf
macOS: `caffeinate -dims just load-test 3h 50`.

## Im Testbetrieb gegen eine echte Installation (V1-18)

1. **Server-Metriken einschalten**, nur auf Loopback: `compose.override.yaml` neben `compose.yaml` in
   `deploy/compose/`:

   ```yaml
   services:
     server:
       environment:
         SERMON_METRICS_ADDR: ":9464"
       ports: ["127.0.0.1:9464:9464"]
   ```

   Danach `docker compose up -d server`. Der Listener liefert nur Werte der Instanz, keine Mandantendaten.

2. **Ein Konto zum Lesen:** ein Mitglied der Organisation mit Rolle `viewer` auf allen Gruppen (`operator`, wenn die
   Organisation Viewern keine Logs zeigt) und nur einem Passwort (das Skript kann keinen zweiten Faktor eingeben). Das Passwort in eine Datei mit Rechten `0600`. Geht das nach der Richtlinie der
   Organisation nicht, nimmt `LOAD_COOKIE_FILE` stattdessen den Wert des Cookies `__Host-sermon_session` aus einem
   Browser (er läuft nach 30 min ohne Abfrage und spätestens nach 12 h ab; mit Passwort meldet sich das Skript neu an).

3. **Auf 50 Nodes auffüllen** (wenn es weniger echte Hosts gibt): Agent mit dem Simulator bauen – er ist nie in einem
   Release – und auf einem eigenen Rechner (nicht auf dem Server-Host) laufen lassen. In der UI eine Gruppe mit dem
   Modul `simulate` anlegen und ein Enrollment-Token für sie mit so vielen Nutzungen wie virtuellen Nodes:

   ```sh
   cd agent && cargo zigbuild --release --locked -p sermon-agent --features simulate --target x86_64-unknown-linux-musl
   cat > sim.toml <<'EOF'
   [server]
   url = "https://sermon.example.org"
   [paths]
   state_dir = "/var/lib/sermon-sim"
   EOF
   SERMON_ENROLLMENT_TOKEN=sme_… target/x86_64-unknown-linux-musl/release/sermon-agent simulate \
     --config sim.toml --nodes 45 --log-rate 2 --scenario cpu-high --scenario ssh-bruteforce
   ```

   Die Identitäten liegen in `state_dir`; ein Neustart nimmt dieselben Nodes wieder.

4. **Messen** auf dem Server-Host im Verzeichnis `deploy/compose` (Docker-Zugriff für `docker stats`, `du` und `psql`
   im Container), eine Woche lang alle 60 s:

   ```sh
   LOAD_URL=https://sermon.example.org LOAD_EMAIL=viewer@example.org LOAD_PASSWORD_FILE=~/.sermon-load \
   LOAD_METRICS_URL=http://127.0.0.1:9464/metrics \
     nohup sh scripts/load-report.sh sample ~/load/woche-1 7d 60 >/dev/null 2>&1 &
   sh scripts/load-report.sh report ~/load/woche-1 --limit 'fleet.online.p50>=50' > ~/load/woche-1.md
   ```

   Den Bericht kann man jederzeit schreiben, auch während die Messung läuft. Ohne Docker-Zugriff entfallen
   Ressourcen und Volumes (`LOAD_DOCKER=0`); die Datenbank erreicht das Skript dann mit `LOAD_PSQL`, einem Befehl, der
   SQL auf stdin liest, z. B. `LOAD_PSQL='psql -qtAX -F "$(printf "\t")" "service=sermon"'` mit einer Rolle, die
   `pg_read_all_stats` und Lesezugriff auf die Metrik-Tabellen hat. Ressourcen des Agents auf echten Hosts misst
   [`scripts/agent-budget.sh`](../../../scripts/agent-budget.sh).
