# Spike M0-09: TimescaleDB-Ingest

Wegwerf-Code zur Prüfung der Abnahmekriterien aus [ADR 0001](../../docs/adr/0001-zeitreihen-speicher-timescaledb.md).
Er gehört nicht zum Produkt, wird nicht gebaut, nicht gelintet und nicht gepflegt. Die Ergebnisse stehen im ADR, die
Rohausgaben in `results/`.

Nachstellen (Docker nötig; die Werte gelten für eine eigene Spike-DB, nicht für die Entwicklungs-DB):

```bash
docker run -d --name sermon-spike --cpus 4 --memory 6g -p 127.0.0.1:55433:5432 \
  -e POSTGRES_USER=sermon -e POSTGRES_DB=spike -e POSTGRES_PASSWORD=<zufällig> -e TIMESCALEDB_TELEMETRY=off \
  <timescale-image aus deploy/dev/compose.yaml>
DSN="postgres://sermon:<passwort>@127.0.0.1:55433/spike?sslmode=disable"
go run . -dsn "$DSN" schema
go run . -dsn "$DSN" -nodes 50 -series 400 seed
go run . -dsn "$DSN" -duration 20m -container sermon-spike load
go run . -dsn "$DSN" backfill && go run . -dsn "$DSN" queries && go run . -dsn "$DSN" rls
docker rm -f sermon-spike
```
