# Operations

What to do and know while SerMon runs: self-monitoring, retention, deletion, enrollment, updates, certificates and
capacity. Installing and updating the server: [installation](../installation.md); backups:
[backup and restore](../backup.md) (both in German); all variables: [reference](configuration.md).

## Self-monitoring

The server checks itself every minute ([ADR 0027](../../adr/0027-server-ereignisse-und-self-monitoring.md)):

| Check | Levels |
|---|---|
| Ingest lag: how old an agent's latest batch is when it arrives | warning from 60 s, critical from 10 min |
| Dropped samples (series limit, implausible times, late windows, unknown metrics) | warning if any are added within 15 min |
| Rare security events (sign-ins, sudo, accounts, system) dropped by a node's rate limit | warning |
| Size of the database against `SERMON_DATABASE_SIZE_LIMIT` | warning from 80 %, critical from 90 % |
| Loki unreachable: the ingest drops logs (metrics, events and alerts go on) | warning |
| Expiry of the agent CA: when the first trusted CA expires | warning from 90 days before the expiry, critical from 30 days |

- On every change of level – the all-clear too – every superadmin gets a mail over the instance's SMTP. In the UI
  superadmins see open problems as a band below the header. Organizations see nothing of it.
- Without `SERMON_DATABASE_SIZE_LIMIT` the server doesn't check the size. It knows only the size of the database, not
  the free space: set the limit below the space of the disk and **install an agent on the server host** – its alerts
  on disk, memory and CPU cover the volumes as well.
- **Health:** `curl -fsS https://sermon.example.org/readyz` answers with the state of database and Loki
  (`{"status":"ready","checks":{…}}`, otherwise HTTP 503); `/healthz` only says the process runs.
- **`/metrics`** (Prometheus format, without sign-in, instance values only) is off by default. In the compose e.g. with
  a file `deploy/compose/compose.override.yaml`, on the host's loopback only:

  ```yaml
  services:
    server:
      environment:
        SERMON_METRICS_ADDR: ":9464"
      ports: ["127.0.0.1:9464:9464"]
  ```

  Then `curl -s http://127.0.0.1:9464/metrics`. The server warns at startup because the address is not loopback inside
  the container – it is reachable only through the port on the host's `127.0.0.1`. Important counters:
  `sermon_server_ingest_lag_seconds`, `sermon_server_agent_sessions`, `sermon_server_ingest_rejected_samples_total`,
  `sermon_server_ingest_dropped_series_total`, `sermon_server_ingest_dropped_log_entries_total`,
  `sermon_server_ingest_uncounted_log_entries_total` (lines in Loki the log histogram misses),
  `sermon_server_notify_failed_total`, `sermon_server_notify_blocked_total`.

## Retention

| Data | Kept | Configurable |
|---|---|---|
| Metrics, 1 s | 15 minutes in the server's memory | no |
| Metrics, 10 s / 1 min / 1 h | 24 hours / 30 days / 1 year | no |
| Logs (Loki) | 14 days by default | per organization |
| Counts of the log lines for the histogram (database) | as the logs, at most 14 days | with the logs |
| Security events, alert history | `SERMON_EVENT_RETENTION`, 90 days by default | operator and per organization |
| Audit logs | `SERMON_AUDIT_RETENTION`, 2 years by default | operator |

- **Per organization** admins set the retention under "Organization": logs 1 to 14 days, security events and alert
  history 7 to 365 days ([retention](../../help/en/concepts/retention.md)). As a superadmin you bound that per
  organization with the quota "retention cap"; a lowered quota deletes nothing.
- **Without a setting of their own** organizations keep security events and alert history 90 days by default
  (`SERMON_EVENT_RETENTION`, 7 to 365 days). A brute force against many nodes makes one row per attempt: in the load
  test with 50 nodes, attacked a third of the time, that was 30,000 events and 16.6 MiB per hour, about 35 GiB at
  90 days and 140 GiB at a year. Plan the space for a longer retention. The server deletes what is older per
  organization every hour and frees whole months once no organization keeps them.
- **Loki** keeps logs at most `retention_period: 336h` (`deploy/compose/loki.yaml`); that is the instance's limit.
  Don't raise it: the organizations' settings don't go beyond it. Loki deletes about once a day, so logs may stay
  visible up to a day longer. The counts of the log lines the histogram reads long ranges from
  ([ADR 0044](../../adr/0044-log-zaehlungen-fuer-das-histogramm.md), in German) go after 14 days (whole days) in the
  database and with an organization's logs up to its period.
- Audit logs lose old entries from the start of the chain; an entry `audit.prune` keeps the chain verifiable.
- The full list with every period (sessions, invitations, delivery log, anomalies …) is in the
  [GDPR documentation template](../../privacy/en/README.md).

## Deletion: nodes, organizations, accounts

All deletion jobs run hourly; the periods are in the [reference](configuration.md#periods).

- **Delete a node** (admins of the group, with the host name and a renewed confirmation): the node is deactivated at
  once and its certificates revoked. After `SERMON_NODE_DELETION_GRACE` (7 days by default) the server deletes its
  logs, metrics, anomalies, events, alerts and sign-in sources; until then the deletion can be cancelled
  ([ADR 0041](../../adr/0041-nodes-deaktivieren-loeschen.md)). On the host afterwards run `sermon-agent uninstall --yes`
  or remove the package.
- **Delete an organization** (superadmin, page "Organizations"): "schedule deletion" locks it at once – members signed
  out, agents disconnected, tokens and invitations void – and deletes it after `SERMON_ORG_DELETION_GRACE` (30 days by
  default) with all its data and the accounts without another membership. The admins get mails when it is locked,
  unlocked and 7 days before the deletion. While it is locked its audit log can be downloaded with the hash chain
  ([ADR 0029](../../adr/0029-organisationen-verwalten-sperren-loeschen.md)).
- **Accounts:** the server deletes an account without a membership after `SERMON_ACCOUNT_DELETION_GRACE` (30 days by
  default; superadmins never). On request a superadmin deletes it at once (page "Organizations").

## Enrollment tokens

Operators create tokens under "Nodes → Add node" for a group: valid for 5 minutes to 7 days, default 1 hour, 1 to 1000
uses, default 1. The server stores only the hash; a token can be revoked.

- For a rollout to many hosts take a token with a matching number of uses and a short validity, not one for weeks.
- Every use triggers the rule `security.enrollment.token_used` (warning, T3) – that's how a stolen token shows. For a
  planned mass rollout create a silence beforehand instead of switching the rule off.
- When a host is reinstalled, the server knows its `machine-id` and refuses; "allow takeover" on the node page (tab
  "Identity") opens an hour for it ([identity](../../help/en/concepts/node-identity.md)).
- Pass a token to the script only by `SERMON_ENROLLMENT_TOKEN` or `--token-file`, never as an argument
  ([hardening](hardening.md#options-of-installsh)).

## Updates of agents and hosts

**Order for a new version:** update the server first ([installation](../installation.md)), then the agents. Agents
never become newer than their server (except in the channel `beta`).

- **Channels:** `stable` is the server's version – the signed binaries directly in `SERMON_AGENT_DOWNLOADS_DIR`
  (compose: in the release's image, `/srv/downloads`); `beta` are the same file names in `/srv/downloads/beta/`
  (mounted with `compose.override.yaml`, [installation](../installation.md#kanal-beta)). The server reads them at
  startup and every 5 minutes and checks their signatures ([ADR 0036](../../adr/0036-self-update-und-paket-repos.md)).
- **Installed by `install.sh`:** operators start "update agent" on the node page, or the group's update policy does it
  in its window (`agentUpdate`). The helper checks the signature, replaces the binary and puts the old one back if the
  new one doesn't connect within 2 minutes. It renews itself with the agent. Requires `allow_self_update = true`.
- **Installed by package** (`--repo`, .deb, .rpm, .apk): the package manager, also through the update policy.
- **Package updates of the hosts:** by hand or by update policy as a rollout with `maxParallel`, health check and
  "stop at the first failure" ([update policy](../../help/en/concepts/update-policy.md)). On suspicion of a taken-over
  account, admins pause all updates of the organization (page "Updates"). Every run is in the audit log
  (`node.command`, `node.run_ended`).

## Certificates and keys

- **Agent certificates are valid for 30 days.** After two thirds the agent creates a new key itself and fetches a new
  certificate over the stream; the old one stays valid for 1 hour. Nothing to do. If the certificate expires while
  the node is offline (after 10 to 30 days without a connection, depending on the last rotation), it must be enrolled
  again.
- **Rotation on demand** (suspicion of a copied key): on the node page, tab "Identity" – rotate, revoke only, unlock
  ([identity](../../help/en/concepts/node-identity.md)).
- **The server certificate of the agent API is valid for 7 days** and renewed automatically in memory, for the names in
  `SERMON_AGENT_HOSTNAMES`.
- **Agent CA:** the root certificate is valid for 10 years, the intermediate CA that issues all certificates for
  2 years from its creation (at `init` or the last CA rollover). The server doesn't renew it by itself; after the
  expiry agents and server refuse each other, and every host must be enrolled again. The self-monitoring warns the
  superadmins from 90 days before the expiry (mail and band in the UI, critical from 30 days). The **CA rollover** on
  the page **Agent CA** (superadmins, next to "Organizations", [help](../../help/en/pages/agent_ca.md)) shows expiry
  and state and runs it with a renewed confirmation:
  1. **Start the rollover:** a new CA issues all certificates from then on; the agents receive a trust bundle with
     both roots when they connect next and fetch a certificate of the new CA at their next rotation. Existing agents
     keep connecting, new ones enroll as usual.
  2. **Retire the previous CA** once it has no valid certificates left (30 days after the start at the latest). Only
     then does the agent API's server certificate come from the new CA, too.

  So start the rollover 30 days before the expiry at the latest, better at the first warning. Both steps are in the
  instance audit log (`pki.rollover_started`, `pki.authority_retired`).
- **Master keys:** never replace, shorten or reorder them. The format knows several keys (newest first); a tested
  procedure to change them with resealing doesn't exist in v1.0.
- **Release and package keys** and their rotation: [installation](../installation.md) and
  [ADR 0036](../../adr/0036-self-update-und-paket-repos.md).

## Capacity

- **Guideline for 50 nodes:** 4 vCPU, 8 GB RAM, 200 GB NVMe SSD (KONZEPT §22). The test operation with the release
  candidate (V1-18) confirms or corrects it; the result will be added here.
- **Load test before it (V1-08, V1-21 to V1-23,** [load test](load-test.md)**):** 50 virtual nodes with about 110
  series and 2 log lines per second used just under a quarter of a core on average and at most 2.0 GiB of memory, without lag.
  Extrapolated to a year about 55 GiB: security events with a brute force on every node a third of the time about
  33 GiB (90 days), Loki 11 GiB, the compressed metrics about 7 GiB. Measure your own instance with
  `scripts/load-report.sh`.
- At most 2,000 active series per node and 250,000 per organization (T9); beyond that the ingest drops new series
  (self-monitoring "dropped samples"). Superadmins set quotas per organization (nodes, members, samples per second,
  series) on the page "Organizations".
- Loki is limited to 1 GiB of memory (`SERMON_LOKI_MEMORY`, `SERMON_LOKI_GOMEMLIMIT`). Measured with 50 nodes and 143
  log lines per second: Go heap at most 418 MiB, RSS 599 MiB; the container counts up to 793 MiB with the page cache.
  More nodes or logs are not measured: raise both together ([installation](../installation.md)) and watch Loki's
  memory.
- The **log histogram** reads the counts of the database for ranges from 100 minutes on without a text or units
  ([ADR 0044](../../adr/0044-log-zaehlungen-fuer-das-histogramm.md), in German): measured with 50 nodes and 14 days of
  counts (6 combinations of source and severity per node and minute, 6 million rows, 167 MiB), over 14 days p95 42 ms,
  over 2 days (minute table) p95 0.5 s; the time grows with nodes × combinations. With a text or units Loki counts
  every line, about 0.7 s per hour of logs at 50 nodes and 143 lines per second; beyond 30 s the server gives up and
  the UI asks for a shorter range. After the update to v1.0 the histogram counts from the update on only.
- The agent itself stays below its unit's `MemoryMax=128M` and `CPUQuota=20%`.

## Backups and data protection

- [Backup and restore](../backup.md) (in German): `sermon-server backup` (encrypted, without metrics), restore,
  pgBackRest for metrics, the Loki volume. A backup before every update.
- [GDPR documentation template](../../privacy/en/README.md): which personal data SerMon keeps, periods, technical and
  organizational measures. Whoever changes periods adapts their own copy.
