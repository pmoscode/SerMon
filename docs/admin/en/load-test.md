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
| Ingest: samples, batches, log lines, security events per second, lag, dropped entries | the server's `/metrics` ([ADR 0011](../../adr/0011-server-metriken-endpunkt.md)) | rates, lag p50/p95/max, entries dropped in the window |
| Read times of the UI: fleet, sparklines, node, charts over 1 h, 24 h and 30 days, logs, log histogram | web API with a real session | p50, p95, maximum, errors |
| Size of the database per part (raw values, 1-min and 1-h values, anomalies, rest, largest tables), WAL, checkpoints, failed jobs, security events | SQL | growth per hour (whole window and second half) |
| Volumes of PostgreSQL (with the WAL) and Loki | `du` in the volume | growth per hour |

**Extrapolation:** every part grows as in the second half of the window (after the start-up) up to its retention –
raw values 24 h, `metrics_1m` and anomalies 30 days, `metrics_1h` 365 days, Loki 14 days, the rest of the database
without a limit. The raw values are computed from the chunks compressed already (22 h compressed, the newest 2 h not);
without any yet, the report takes the growth of the window over 24 h as an upper bound. The WAL counts with
`max_wal_size`. `--horizon DAYS` changes the second point in time (default 365).

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
```

The script builds what the demo builds, and the agent with the simulator, and starts the production compose with the
E2E overlay and `deploy/load/compose.yaml` as its own project `sermon-load` (its own ports and network; a demo can run
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
