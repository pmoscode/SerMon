# Troubleshooting

Typical problems with the commands that narrow them down. Commands for the server run in `deploy/compose/`, those
for a host as root on the host. Under OpenRC use the commands in the section [OpenRC](#openrc) instead of
`systemctl`/`journalctl`.

## First looks

```sh
# Server
docker compose ps                                   # all services up? migrate: "exited (0)"
docker compose logs --since 1h server | grep -v '"level":"INFO"'
curl -fsS https://sermon.example.org/readyz          # {"status":"ready","checks":{"database":"ok","loki":"ok"}}

# Host
systemctl status sermon-agent
journalctl -u sermon-agent --since -1h
sudo -u sermon sermon-agent helper status            # root helper reachable? what does it allow?
```

Superadmins also see problems of the instance as a band in the UI and by mail
([self-monitoring](operations.md#self-monitoring)).

## The agent doesn't connect

The agent's log names the reason in a line `session failed: …`:

| Message (excerpt) | Cause | Remedy |
|---|---|---|
| `the server rejected this agent's certificate (revoked, node locked or removed)` | The certificate is on the denylist: node deactivated, deleted, locked or key revoked | Node page, tab "Identity": unlock if the lock is harmless; otherwise enroll again (below) |
| `the agent certificate has expired` | offline too long, the automatic rotation didn't get its turn | enroll again (below) |
| `certificate not valid for name …` | The name in `agent_url` is missing in `SERMON_AGENT_HOSTNAMES` | add it to `SERMON_AGENT_HOSTNAMES`, `docker compose up -d` |
| `UnknownIssuer` | Port 8443 isn't answered by this server's agent API: a TLS proxy in front, or a newly set-up instance with a new agent CA | port 8443 must reach the server directly; after a new `init` enroll the hosts again |
| `certificate not valid yet`, `certificate expired` | the host's clock is wrong – or the agent CA has expired | synchronize the time (below); check the expiry on the page "Agent CA" ([operations](operations.md#certificates-and-keys)) |
| `connection refused`, timeout | firewall, wrong port | check port 8443 from the host (below) |

**Reachability and certificate of the agent API** (it comes from the agent CA, not a public one – that's right):

```sh
openssl s_client -connect sermon.example.org:8443 -servername sermon.example.org </dev/null 2>/dev/null \
  | openssl x509 -noout -subject -ext subjectAltName -dates
```

The name from `agent_url` in `/etc/sermon/agent.toml` (by default the host of `url` with port 8443) must be listed
under `subjectAltName`.

**Clock:** `timedatectl` or `chronyc tracking`. Certificates are valid from 5 minutes before they are issued; values
more than 5 minutes in the future or older than 24 hours are dropped by the server
(`sermon_server_ingest_rejected_samples_total`). The metrics `sermon_time_synchronized` and `sermon_time_offset_seconds`
show every node's clock.

**Enroll again:** create a token for the node's group in the UI; if the host was enrolled before (same `machine-id`),
"allow takeover" on the node page first (one hour). Then run the install command from the UI with `--force`, or
directly:

```sh
 export SERMON_ENROLLMENT_TOKEN=sme_…
sudo --preserve-env=SERMON_ENROLLMENT_TOKEN -u sermon sermon-agent enroll --force
systemctl restart sermon-agent
```

### The enrollment fails

| Message | Remedy |
|---|---|
| `the enrollment token is invalid, expired, revoked or used up` | Create a new token; the server deliberately doesn't say which case it is. |
| `this host (same machine-id) is already registered in the organization` | "Allow takeover" on the existing node, then use a token of **its** group. Cloned VMs need a `machine-id` of their own. |
| `too many enrollment attempts` | wait a minute (rate limit per address). |
| `no server certificate matches the pinned fingerprint …` | determine the fingerprint again (below). |
| `agent.toml points to …, not …` (from `install.sh`) | The configuration points to another server: replace it with `--force` if that is intended. |
| `signature check failed` (from `install.sh`) | Binary and signature in `downloads/` don't match the release key or the server's version. Install nothing, check where they came from. |

Fingerprint of a self-signed certificate for `--ca-fingerprint`:

```sh
openssl s_client -connect sermon.example.org:443 -servername sermon.example.org </dev/null 2>/dev/null \
  | openssl x509 -noout -fingerprint -sha256
```

`sha256 Fingerprint=AB:CD:…` becomes `--ca-fingerprint sha256:AB:CD:…`.

## The node is connected, but metrics are missing

- Collect locally without sending: `sudo -u sermon sermon-agent collect --seconds 5`.
- In `/metrics` ([operations](operations.md#self-monitoring)): `sermon_server_ingest_rejected_samples_total`
  (implausible time or rate limit), `…_dropped_series_total` (series limit), `…_unknown_samples_total` (metric not in
  the catalog, e.g. an agent newer than the server), `…_late_samples_total`.
- The organization's quotas (samples per second and node, series per node) on the page "Organizations". When the
  server throttles, the agent says so in its log.
- High ingest lag: check the load of server and database (`docker stats`).

## Logs are missing

- **Forbidden on the host:** `allow_logs = false` in `agent.toml` – the log settings then show "allow_logs = false" at
  the node. Log files outside `log_file_prefixes` are never read.
- **Off in the organization:** "Organization → Log settings", switch "send logs" per group, minimum severity, excluded
  units ([log settings](../../help/en/concepts/log_settings.md)). If an agent refuses the settings (e.g. a regex), the
  reason is under "state per node".
- **Loki:** without `SERMON_LOKI_URL` the server warns at startup (`SERMON_LOKI_URL is not set`). If Loki is
  unreachable, the self-monitoring reports "The server drops log entries"; then check `docker compose logs loki` and
  `/readyz`.
- **Security events are missing** (no journald, e.g. Alpine): the node page shows the source; see
  [source of security events](../../help/en/concepts/security-source.md) and `install.sh --grant-log-access`.
- Logs older than the organization's retention: Loki deleted them.

## Modules don't run

The node page shows state and reason in the tab "Modules". The agent checks the configuration itself; the reason
names only what the server sent.

| State | Meaning | Check |
|---|---|---|
| Forbidden by the host (`vetoed`) | `agent.toml` forbids it: `allow_modules = false`, or a target lies outside `module_targets`, `module_hosts`, `module_ports`, `module_sockets`, `module_file_prefixes`; a container runtime's socket without `allow_docker_socket` | adapt `[local]` ([hardening](hardening.md#local-vetoes-in-agenttoml)), restart the agent. Symlinks must lie in a prefix with their target, too. |
| Configuration refused (`invalid_config`) | The setting doesn't fit the module's schema in the agent | read the reason; if the agent is older than the server, it doesn't know new fields – update the agent. |
| Error (`error`), timed out (`timeout`) | The service doesn't answer as expected: wrong credentials, missing rights, service down, too slow | read the reason; if a right is missing, the module names the `GRANT` needed. Check the service from the host. |
| Hung (`hung`) | A run doesn't return (a check at startup over 30 s too); a new one starts only after it | check name resolution and the service's socket; if it persists, read `journalctl -u sermon-agent` and restart the agent. |
| Crashed (`crashed`) | A fault in the module; it starts again after a pause, the agent and other modules keep running | report the message from the agent's log as a bug. |

## Root helper and updates

```sh
sudo -u sermon sermon-agent helper status
systemctl status sermon-helper.socket
journalctl -u sermon-helper --since -1d             # every request, refused ones too
journalctl -u 'sermon-helper-run-*' --since -1d     # the runs (package manager, reboot, self-update)
```

| Symptom | Check |
|---|---|
| `helper status` reports that `sermon-helper` is unreachable | Helper not installed or switched off (`--no-helper`, `systemctl disable --now sermon-helper.socket`): run `install.sh` again without `--no-helper`. |
| Every request refused, `unsafe configuration` in the journal | `/etc/sermon/helper.toml` or `/etc/sermon` doesn't belong to root or is writable by group/others. File: `chown root:root`, `chmod 0644`; directory: `chown root:sermon`, `chmod 0750`. |
| Buttons "update now", "reboot" disabled | The host forbids it (`allow_remote_updates`, `allow_remote_reboot`); the UI names the reason. |
| A rollout skips nodes | offline, without helper, forbidden by the host, end of window; the rollout shows the reason. |
| Nothing starts any more | Updates of the organization paused (notice at the top of the page "Updates"), the policy's window not open yet, or `maxParallel` reached. |
| No agent update offered | installed by package (then the package manager), no newer signed version in the channel (`downloads/` or `downloads/beta/`), `allow_self_update = false`. |
| Agent update "rolled back" | The new version didn't connect within 2 minutes: read the agent's log after the restart. |

## Alerts don't arrive

1. **Is there an alert?** Page "Alerts". If not: rule switched off, threshold not reached, or a maintenance window for
   "offline"/"reboot".
2. **Muted?** A silence or a maintenance window suppresses the notification only, the alert stays visible.
3. **Where to?** As soon as there is a single policy (a disabled one too), alerts go through policies only.
4. **Delivery:** "Notifications → Log" shows every attempt with its error (only the target's host, never credentials).
   **Send test** on the channel delivers at once.
5. **Blocked by the SSRF protection** (`target address not allowed`, `plain http only to allowlisted targets`,
   `redirect to another host`, counter `sermon_server_notify_blocked_total`): if the target is on the internal network,
   add its network to `SERMON_OUTBOUND_ALLOWLIST` and run `docker compose up -d`. Public targets only over `https://`;
   redirects only to the same host.
6. **E-mail channels** use the instance's SMTP; at most 100 mails per hour per organization
   (`sermon_server_notify_mails_deferred_total`). A locked organization sends no notifications.
7. **Mails to superadmins** (self-monitoring) and invitations: check the SMTP variables; errors are in the server's log.

## Renewed confirmation (step-up) fails

- The confirmation holds for 15 minutes; after that SerMon asks again before the next sensitive action
  ([step-up](../../help/en/concepts/step-up.md)).
- Five wrong codes or passwords lock the account for increasing periods (up to 15 minutes). Check the phone's clock:
  TOTP allows only ±1 step (30 s).
- **Over OIDC:** SerMon asks the provider for a fresh sign-in (`max_age=0`). The server's log then has
  `oidc flow failed` with the reason: `no fresh login at the provider (auth_time)` – the provider reports no new
  sign-in, it must honour `max_age`; `provider login without a second factor for a user with one` – the user has a
  second factor in SerMon, the provider's sign-in named none (`SERMON_OIDC_MFA`).
- No passkeys at all: the UI runs under an IP address or a name without a dot (`passkeys are off` in the log).

## Migrations

- The server doesn't start, `migrate` ends with an error: `docker compose logs migrate`. The server waits for a
  successful `migrate`.
- **Development databases:** `detected … missing (out-of-order) migrations` means the database has a higher migration
  than one that was added – typical after switching between branches. SerMon applies migrations in order only. For
  `just dev`: `just dev-reset` (deletes the development data). In production this doesn't happen as long as only
  releases are installed.

## OpenRC

Under OpenRC (Alpine) no logger collects the agent's output. To look, stop the service and start the agent in the
foreground:

```sh
rc-service sermon-agent status
rc-service sermon-agent stop
su -s /bin/sh -c '/usr/local/bin/sermon-agent run --config /etc/sermon/agent.toml' sermon   # package: /usr/bin/
rc-service sermon-agent start
```

The helper writes to syslog through `logger` (`grep sermon-helper /var/log/messages`).
