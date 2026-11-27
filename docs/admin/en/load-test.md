# Load test and proof of performance

SerMon comes with two scripts to measure the load of an installation:

- [`scripts/load-report.sh`](../../../scripts/load-report.sh) **measures** a running installation at fixed intervals
  and writes a Markdown report with limits, pass/fail and an extrapolation of the disk space to 7 days and to the
  retention. It needs only `sh`, `awk`, `curl` and – if available – Docker on the server host; whatever it can't
  reach is "n/a" in the report.
- [`scripts/load-test.sh`](../../../scripts/load-test.sh) (`just load-test`) **creates** the load on the development
  machine: the production compose with virtual nodes, then the measurement (V1-08).

The proof of the guideline (KONZEPT §22: 50 nodes on 4 vCPU, 8 GB RAM, 200 GB SSD) comes from the test operation
(V1-18) with exactly this script.

## What is measured

| Part | Source | In the report |
|---|---|---|
| CPU and memory of `server`, `postgres`, `loki`, `caddy` (and e.g. the simulator) | `docker stats` | mean, p95, maximum; CPU in % of one core |
| Memory of Loki as Go sees it: heap, target of the next GC, RSS, collections per minute | Loki's `/metrics` (`LOAD_LOKI_METRICS_URL`; the load test publishes only that path on 127.0.0.1:19310) | mean, maximum, next to the memory of the container |
| Ingest: samples, batches, log lines, security events per second, lag, dropped entries | the server's `/metrics` ([ADR 0011](../../adr/0011-server-metriken-endpunkt.md)) | rates, lag p50/p95/max, entries dropped in the window |
| Read times of the UI: fleet, sparklines, node, charts over 1 h, 24 h and 30 days, logs, log histogram over 1 h, over 1 h with a text (`log_histogram_text`, from Loki), over 2 days (`log_histogram_2d`) and over 14 days (`log_histogram_14d`, the longest range of the UI; both from the counts, ADR 0044) | web API with a real session | p50, p95, maximum, errors |
| Size of the database per part (raw values, 1-min and 1-h values with their compression, anomalies, counts of the log lines, security events, rest, largest tables), WAL and `pg_wal`, checkpoints, failed jobs | SQL | growth per hour (whole window and second half) |
| Volumes of PostgreSQL (with the WAL) and Loki | `du` in the volume | growth per hour |

**Extrapolation:** every part grows as in the second half of the window (after the start-up) up to its retention –
raw values 24 h, `metrics_1m` and anomalies 30 days, the counts of the log lines 14 days (as measured after a
backfill), `metrics_1h` 365 days, security events with the alert history
`--event-days DAYS` (default 90 like `SERMON_EVENT_RETENTION`), Loki 14 days, the rest of the database without a
limit. The three tiers of the metrics are computed from the chunks compressed already: raw values with the newest 2 h
uncompressed, `metrics_1m` 16 h (6 h until the compression and one chunk of 10 h), `metrics_1h` 192 h (a day and one
chunk of 7 days), the rest at the ratio of the compressed chunks; `metrics_1h` without one of its own at the ratio of
`metrics_1m`. Without a compressed chunk the report takes the linear growth as an upper bound. The WAL counts with
the largest `pg_wal` measured, without a measurement with `max_wal_size`. `--horizon DAYS` changes the second point in
time (default 365).

**Limits:** `load-report.sh limits` shows the defaults (the guideline with room for the host, the criteria of the spike
in [ADR 0001](../../adr/0001-zeitreihen-speicher-timescaledb.md), nothing dropped). Own ones with
`--limit 'KEY.STATISTIC<=VALUE'` or `>=`; a limit replaces the default of the same key. The report ends with a
counter-check (every limit kept must fail just below the measurement) and the result; the exit code is 1 if a limit
is exceeded.

## On the development machine

```sh
just load-test 3h 50                 # duration and number of nodes
SKIP_BUILD=1 just load-test 10m 50   # reuse the builds and secrets of the last run
KEEP=1 …                             # leave the environment running afterwards (https://localhost:19443)
COMPRESS_AGGREGATES=0 …              # don't compress metrics_1m by hand at the end (default: 1)
BACKFILL_DAYS=0 …                    # don't fill in log counts in advance (default: 14 days)
```

Loki takes no lines older than 7 days, and writing 14 days of logs would take as long as 14 days of operation. So that
the 14-day histogram still reads 14 days, the script waits 11 min after the start (`BACKFILL_WARMUP` in seconds) and
then fills `BACKFILL_DAYS` days of counts in front of the first measured minute by SQL: per node the lines per minute
and severity of the last 10 min, in all three sources (three times the rows of the simulator, which has the journal
only), the hourly table from them, older chunks compressed as their policy would. The report names it under
"Backfill" and takes the counts as measured instead of linear then. The lines in Loki stay those of the run; the
queries with a text (`log_histogram_text`) therefore measure Loki over one hour only.

The policy of `metrics_1m` reaches a chunk only 6 h after it ended; a short run would have nothing compressed. So the
script compresses every chunk of `metrics_1m` by hand after the sampling and takes a last sample of the database. A
chunk of a short run holds fewer rows per series than one of 10 h; the ratio is rather too poor than too good.

The script builds what the demo builds, and the agent with the simulator, and starts the production compose with the
server built from source (`source.yaml`), the E2E overlay and `deploy/load/compose.yaml` as its own project `sermon-load` (its own ports and network; a demo can run
next to it). It creates an admin, a group `load` with the module `simulate` and an enrollment token with enough uses,
and starts the virtual nodes in one container with the scenarios `cpu-high`, `ssh-bruteforce` and `flapping` and 2
synthetic journal lines per second and node (`SCENARIOS`, `LOG_RATE`). A virtual node sends about as many series as a
real host (about 110). Report and raw samples go to `deploy/load/.runs/<time>/`. For long runs on macOS:
`caffeinate -dims just load-test 3h 50`.

## In the test operation against a real installation (V1-18)

1. **Turn on the server's metrics**, on loopback only: `compose.override.yaml` next to `compose.yaml` in
   `deploy/compose/`:

   ```yaml
   services:
     server:
       environment:
         SERMON_METRICS_ADDR: ":9464"
       ports: ["127.0.0.1:9464:9464"]
   ```

   Then `docker compose up -d server`. The listener only serves values of the instance, no tenant data.

2. **An account for reading:** a member of the organization with the role `viewer` on all groups (`operator` if the
   organization shows no logs to viewers) and a password only (the script can't enter a second factor). Put the
   password into a file with mode `0600`. If the organization's policy doesn't allow that, `LOAD_COOKIE_FILE` takes
   the value of the cookie `__Host-sermon_session` from a browser instead (it ends after 30 min without a request and
   after 12 h at the latest; with a password the script signs in again).

3. **Fill up to 50 nodes** (if there are fewer real hosts): build the agent with the simulator – it is never part of
   a release – and run it on a machine of its own (not on the server host). In the UI, create a group with the module
   `simulate` and an enrollment token for it with as many uses as virtual nodes:

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

   The identities live in `state_dir`; a restart takes the same nodes again.

4. **Measure** on the server host in the directory `deploy/compose` (Docker access for `docker stats`, `du` and `psql`
   in the container), for a week every 60 s:

   ```sh
   LOAD_URL=https://sermon.example.org LOAD_EMAIL=viewer@example.org LOAD_PASSWORD_FILE=~/.sermon-load \
   LOAD_METRICS_URL=http://127.0.0.1:9464/metrics \
     nohup sh scripts/load-report.sh sample ~/load/week-1 7d 60 >/dev/null 2>&1 &
   sh scripts/load-report.sh report ~/load/week-1 --limit 'fleet.online.p50>=50' > ~/load/week-1.md
   ```

   The report can be written at any time, also while the measurement runs. Without Docker access the resources and
   volumes are left out (`LOAD_DOCKER=0`); the script then reaches the database through `LOAD_PSQL`, a command that
   reads SQL on stdin, e.g. `LOAD_PSQL='psql -qtAX -F "$(printf "\t")" "service=sermon"'` with a role that has
   `pg_read_all_stats` and read access to the metric tables. The resources of the agent on real hosts are measured by
   [`scripts/agent-budget.sh`](../../../scripts/agent-budget.sh).
