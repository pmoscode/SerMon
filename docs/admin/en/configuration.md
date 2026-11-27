# Server configuration (reference)

`sermon-server` reads its settings from environment variables only. Every variable `SERMON_X` also works as a file:
`SERMON_X_FILE=/path` reads the value from the file (without the trailing line break), e.g. from a Docker secret.
Setting both is an error. The server checks everything at startup and reports all errors at once; error messages
never show passwords or keys.

In [`deploy/compose`](../../../deploy/compose/), `compose.yaml` sets most values itself; what you change is in `.env`
(template `env.example`, see [installation](../installation.md), in German). The column "compose" says what applies
there. A test (`server/cmd/sermon-server/admin_docs_test.go`) requires every variable of the server to be listed here
and the defaults to match the code.

Related: [hardening](hardening.md) (which values affect security), [operations](operations.md).

## General

| Variable | Default | compose | Meaning |
|---|---|---|---|
| `SERMON_MODE` | `production` | – | `development` allows running without SMTP (invitation links then show in the UI and prove no address), `http://` origins and SMTP without TLS. **Never in production**; the server warns at startup. |
| `SERMON_HTTP_ADDR` | `:8080` | `:8080` | Address of the web UI and web API (`host:port`), behind Caddy. |
| `SERMON_LOG_LEVEL` | `info` | – | `debug`, `info`, `warn` or `error`. |
| `SERMON_LOG_FORMAT` | `json` | `json` | `json` or `text`. |
| `SERMON_SHUTDOWN_TIMEOUT` | `15s` | – | How long running requests may still take after `SIGTERM`. |

## Database and keys

| Variable | Default | compose | Meaning |
|---|---|---|---|
| `SERMON_DATABASE_URL` | – | `secrets/database_url` | Connection as `sermon_app` (no special rights, RLS applies). Empty: no database, UI and health only. The server refuses roles with superuser or `BYPASSRLS` rights. |
| `SERMON_DATABASE_MIGRATE_URL` | – | only in the service `migrate` | Connection as owner `sermon_owner` for migrations (`sermon-server migrate`, `init`) and the restore. Never belongs in the server container. |
| `SERMON_MASTER_KEYS` | – | file `/var/lib/sermon/master_keys` in the volume `sermon_server-state` | Required with a database. One line `<id>:<base64>` per key, newest first; create it with `sermon-server init --master-keys-file PATH`. **Never replace, shorten or reorder it**, and back it up separately ([backup](../backup.md)). |
| `SERMON_DATABASE_SIZE_LIMIT` | – | from `.env` | Size from which the self-monitoring warns, e.g. `150GiB` (bytes or `KiB`, `MiB`, `GiB`, `TiB`). Empty: not checked. See [operations](operations.md#self-monitoring). |

## Web UI, proxy and outgoing connections

| Variable | Default | compose | Meaning |
|---|---|---|---|
| `SERMON_ALLOWED_ORIGINS` | – | `https://<SERMON_DOMAIN>` | Exact origins of the UI, separated by commas (`https://host[:port]`). The server refuses writing calls with another `Origin`; empty means **all** writing calls are refused. Only `https://` in production. The first origin appears in mails and is the return address of OIDC. Required with SMTP. |
| `SERMON_TRUSTED_PROXIES` | – | Caddy's fixed address as `/32` | CIDRs that may set `X-Forwarded-For`. Only your own reverse proxy – otherwise anyone picks their own address for rate limits and the audit log. |
| `SERMON_OUTBOUND_ALLOWLIST` | – | from `.env` | Internal networks that channels, webhooks and the OIDC provider may reach (`10.20.0.0/16` or addresses, separated by commas). Empty: public addresses only. The server refuses networks that allow everything and ranges with embedded IPv4; the clouds' metadata services stay blocked always ([ADR 0024](../../adr/0024-ausgehende-verbindungen-safehttp.md)). |
| `SERMON_METRICS_ADDR` | – | not set | Own listener for `/metrics` without sign-in (instance values only, no tenant data, [ADR 0011](../../adr/0011-server-metriken-endpunkt.md)). Empty: off. Must differ from `SERMON_HTTP_ADDR` and `SERMON_AGENT_ADDR`; outside loopback the server warns. |
| `SERMON_GEOIP_DB_FILE` | – | from `.env` (`/srv/geoip/…`) | Country database in MaxMind DB format for the security dashboard; the server downloads nothing. |
| `SERMON_GEOIP_ATTRIBUTION` | – | from `.env` | Replaces the licence notice derived from the file (at most 300 bytes); only with `SERMON_GEOIP_DB_FILE`. |

## Agents

| Variable | Default | compose | Meaning |
|---|---|---|---|
| `SERMON_AGENT_ADDR` | – | `:8443` | Address of the mTLS agent API. Empty: off. Needs `SERMON_DATABASE_URL` (the agent CA lives in the database). |
| `SERMON_AGENT_HOSTNAMES` | – | `<SERMON_DOMAIN>` | DNS names or IPs agents reach the agent API under (commas); they are in the server certificate. Required with `SERMON_AGENT_ADDR`. A name in the agents' `agent_url` that is missing here fails at TLS. |
| `SERMON_AGENT_DOWNLOADS_DIR` | – | `/srv/downloads` (`./downloads`) | Signed binaries of agent and helper for `install.sh` and self-updates (channel `stable` directly in it, `beta` in `beta/`). |
| `SERMON_REPO_DIR` | – | `/srv/repo` (`./repo`) | Signed package repositories for `install.sh --repo` under `/repo/`. |

## Logs

| Variable | Default | compose | Meaning |
|---|---|---|---|
| `SERMON_LOKI_URL` | – | `http://loki:3100` | Loki for the nodes' logs ([ADR 0031](../../adr/0031-logs-in-loki.md)); `http(s)://` without credentials, query or fragment. Empty: the server takes no logs and warns at startup. |

## Periods

Durations in Go's format (`720h`, `90m`); there are no days. The jobs run hourly.

| Variable | Default | compose | Meaning |
|---|---|---|---|
| `SERMON_ORG_DELETION_GRACE` | `720h` | from `.env` | Between "schedule deletion" and the deletion of an organization; at least `168h`. |
| `SERMON_NODE_DELETION_GRACE` | `168h` | from `.env` | Between "delete node" and the deletion of its data; at least `24h`. |
| `SERMON_ACCOUNT_DELETION_GRACE` | `720h` | from `.env` | Accounts without a membership are deleted after it (superadmins never); at least `24h`. |
| `SERMON_AUDIT_RETENTION` | `17520h` | from `.env` | How long the audit logs of the organizations and of the instance keep an entry; `2160h` to `87600h`. |

## Mail (SMTP)

Required in production as soon as a database is connected: invitations go out by mail only, so the address is proven.

| Variable | Default | compose | Meaning |
|---|---|---|---|
| `SERMON_SMTP_HOST` | – | from `.env` | Name or IP of the mail server. |
| `SERMON_SMTP_TLS` | `starttls` | `starttls` | `starttls` (STARTTLS required), `tls` (TLS from the first byte) or `none` (only with `SERMON_MODE=development`). |
| `SERMON_SMTP_PORT` | `587` | `587` | `587` by default, `465` with `tls`, `25` with `none`. |
| `SERMON_SMTP_FROM` | – | from `.env` | Sender, e.g. `SerMon <sermon@example.org>`; required. |
| `SERMON_SMTP_USERNAME` | – | from `.env` | Empty: no authentication. Only together with the password. |
| `SERMON_SMTP_PASSWORD` | – | `secrets/smtp_password` | Password of `SERMON_SMTP_USERNAME`. |
| `SERMON_SMTP_CA_FILE` | – | not set | PEM file with the CAs the mail server is verified against instead of the system's. |

## Sign-in over OIDC

Without `SERMON_OIDC_ISSUER` there are only local accounts; the other variables are an error then if they have a
value. Setup: [installation](../installation.md) (in German).

| Variable | Default | compose | Meaning |
|---|---|---|---|
| `SERMON_OIDC_ISSUER` | – | from `.env` | Issuer URL of the provider; only `https://` in production. |
| `SERMON_OIDC_CLIENT_ID` | – | from `.env` | Client ID (at most 255 bytes); required. |
| `SERMON_OIDC_CLIENT_SECRET` | – | `secrets/oidc_client_secret` | Client secret; required. |
| `SERMON_OIDC_NAME` | `SSO` | from `.env` | Label of the button on the sign-in page (at most 40 characters). |
| `SERMON_OIDC_SCOPES` | `email profile` | from `.env` | Scopes besides `openid`; `groups` for the group mappings. |
| `SERMON_OIDC_MFA` | `amr` | from `.env` | When the provider's sign-in counts as two factors: `amr` (the claim names a second factor), `trusted` (always – only if the provider enforces one) or `never`. |
| `SERMON_OIDC_REDIRECT_URL` | first origin + `/auth/oidc/callback` | not set | Return address; must end in `/auth/oidc/callback`. |
| `SERMON_OIDC_JIT` | `true` | from `.env` | Create accounts and memberships by the group mappings; `false` turns it off. |

## Backup and restore only

Only `sermon-server backup` and `restore` read these ([ADR 0042](../../adr/0042-logisches-backup-und-restore.md),
[backup and restore](../backup.md), in German); the server itself never does. `SERMON_MASTER_KEYS` and, for the
restore, `SERMON_DATABASE_MIGRATE_URL` come on top.

| Variable | Default | compose | Meaning |
|---|---|---|---|
| `SERMON_DATABASE_BACKUP_URL` | – | `secrets/database_backup_url`, only in the service `backup` | Connection as `sermon_backup` (read-only, `BYPASSRLS`). |
| `SERMON_BACKUP_RECIPIENTS` | – | `secrets/backup_recipients`, only in the service `backup` | Public age keys, one per line (`sermon-server backup-keygen`). |
| `SERMON_BACKUP_IDENTITY` | – | `secrets/backup_identity`, only for the restore | Secret age key. Never stays on the server. |

## Values outside the server

- **At build time:** `SERMON_RELEASE_KEY` (public release key that `install.sh` checks) and `SERMON_VERSION` are
  compiled into the binary; they are no runtime variables.
- **The compose's `.env`:** `SERMON_DOMAIN`, `SERMON_ACME_EMAIL`, `SERMON_AGENT_PORT`, `SERMON_PROXY_IP`,
  `SERMON_EDGE_SUBNET`, `SERMON_EDGE_IP_RANGE`, `SERMON_LOKI_MEMORY`, `SERMON_LOKI_GOMEMLIMIT` steer the compose, not the
  server ([installation](../installation.md), `deploy/compose/env.example`).
- **On the hosts:** `SERMON_ENROLLMENT_TOKEN` carries the enrollment token to `install.sh` and `sermon-agent enroll`.
  Everything else of the agent is in `/etc/sermon/agent.toml` and `/etc/sermon/helper.toml` ([hardening](hardening.md)).
