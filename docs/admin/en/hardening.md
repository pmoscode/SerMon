# Hardening

SerMon ships with strict defaults. This page shows what the defaults protect, where you restrict them further for a
host or the instance – and what a compromised server can still do. It is based on the
[threat model](../../security/threat-model.md) (boundaries TB1–TB9, threats T…, in German) and KONZEPT §18.

## On the hosts

### What the agent may do

- `sermon-agent` runs as the user `sermon` without capabilities, in the groups `systemd-journal` and `adm` (reading
  the journal and log files). It runs typed commands only, there is no "exec" (T12).
- Its configuration `/etc/sermon/agent.toml` belongs to `root:sermon` with `0640` (directory `0750`). The agent can only
  read it (`ReadOnlyPaths=/etc/sermon`): a compromised agent doesn't loosen its vetoes.
- Identity and buffer are in `/var/lib/sermon-agent` (`sermon`, `0750`; the key `0600`). The buffer holds up to
  24 hours or 200 MiB while the server is unreachable.
- Only the optional root helper `sermon-helper` has root rights – with fixed operations and a veto of its own
  ([below](#root-helper-helpertoml)).

### Local vetoes in `agent.toml`

The server decides what an agent does; the host decides in `[local]` of `/etc/sermon/agent.toml` what of it is
allowed. The server can neither see nor change these values.

| Key | Default | Effect |
|---|---|---|
| `allow_live_view` | `true` | The live view of the processes may be started (on request only, command lines redacted). |
| `allow_logs` | `true` | Log lines go to the server. Security events (SSH, sudo, accounts, reboots) still come with `false`. |
| `log_file_prefixes` | `["/var/log"]` | The organization's log file patterns may read below these directories. Never: the state directory, `/etc/sermon`, `/proc`, `/sys`, `/dev`, the journal's files, `wtmp`, `btmp`, `lastlog` (T81). |
| `allow_modules` | `true` | `false`: no module runs. |
| `module_targets` | `["127.0.0.0/8", "::1/128"]` | Networks (CIDR) or addresses modules connect to (T107). |
| `module_hosts` | `["localhost"]` | Names modules may resolve: a name, `*.example.org` (all names below it) or `*`. Other names never reach the resolver; every address a name points to must lie in `module_targets`. |
| `module_ports` | `[80, 443, 465, 636, 993, 995, 3306, 5432, 6379, 8080, 8443, "9090-9999"]` | TCP ports modules connect to; ranges as text (`"9090-9999"`). |
| `module_sockets` | `["/run", "/var/run"]` | Unix sockets below these directories; never in a directory everyone may write to (`/run/lock`, `/tmp`). |
| `module_file_prefixes` | `["/etc/ssl", "/etc/pki", "/etc/letsencrypt"]` | Files modules read (certificates, CA files, T108). |
| `allow_docker_socket` | `false` | Sockets of container runtimes and VM managers (Docker, Podman, containerd, CRI-O, BuildKit, libvirt, LXD/Incus). Whoever uses them is root on the host (O4). |

Good to know:

- A list **replaces** the default: whoever sets `module_ports = [5432]` allows only 5432 from then on.
- Paths are absolute, without `.` and `..`, and compared by segment. Symlinks are resolved and the target checked
  again: `/etc/letsencrypt/live` as a prefix is not enough because the files there point to `../archive/…`.
- Always blocked for modules, whatever the prefixes say: `/etc/sermon`, `/proc`, `/sys`, `/dev`,
  `/var/lib/sermon-agent`, `/run/sermon` (the helper's socket), the configured state directory and the directory of
  `agent.toml`.
- An unknown key or an invalid value stops the agent (`invalid configuration: …` in the log) – a typo never loosens a
  veto silently.
- The agent reads the file at startup only: `systemctl restart sermon-agent` or `rc-service sermon-agent restart`.
- When a module is refused, the node page shows "Forbidden by the host"; the reason names only what the server sent
  itself, never resolved addresses or paths.

A narrow host that sends only nginx logs, allows no live view and lets a module reach PostgreSQL only:

```toml
[local]
allow_live_view = false
log_file_prefixes = ["/var/log/nginx"]
module_ports = [5432]
```

For a module that checks a service on another host, allow that network and name specifically, not `*`:

```toml
[local]
module_targets = ["127.0.0.0/8", "::1/128", "192.0.2.10/32"]
module_hosts = ["localhost", "db.example.org"]
```

### Root helper (`helper.toml`)

The root helper takes fixed operations only (list and install updates, reboot, self-update of the agent, read SMART),
without free arguments, and only from the user `sermon` (socket `/run/sermon/helper.sock`, `root:sermon 0660`, checked
with `SO_PEERCRED`; T38, T39). What the host allows is in `/etc/sermon/helper.toml`:

| Key | Default | Flag of `install.sh` | Effect |
|---|---|---|---|
| `allow_remote_updates` | `true` | `--no-remote-updates` | Install updates from the distribution's signed sources. |
| `allow_remote_reboot` | `false` | `--allow-remote-reboot` | Reboot on request (needed for "reboot after updates"). Off, because a reboot of the whole fleet would be the worst a compromised server can do. |
| `allow_self_update` | `true` | `--no-self-update` | Replace the agent with a newer version signed with the release key. |
| `allow_prerelease` | `false` | – (by hand) | Self-update to a prerelease (channel `beta`) while the host runs none yet. |
| `allow_smart` | `true` | `--no-smart` | SMART data of the disks with a fixed, read-only `smartctl` (module `smart`). |

- File and directory must belong to `root` and must not be writable by group and others; otherwise the helper refuses
  every request (fail closed). It reads the file on **every** request – a change during an incident applies at once,
  without a restart (T103).
- Without the helper (`install.sh --no-helper`, or the package `sermon-helper` not installed) there is no remote
  control: no updates, reboots, self-updates and no SMART.
- `sudo -u sermon sermon-agent helper status` shows version, package manager and what the helper allows.
- Every request, refused ones too, is in the helper's journal; every run also in the audit log (T41).

### Options of `install.sh`

| Option | Effect |
|---|---|
| `--server` | Public HTTPS address of the server (as in the browser); required. |
| `--token-file` | File with the enrollment token. Otherwise from `SERMON_ENROLLMENT_TOKEN`; `--token` works too, but is then visible in the process list (the script warns). |
| `--ca-fingerprint` | SHA-256 of the self-signed server certificate or of a CA the server sends along (`sha256:AB:CD:…`). |
| `--agent-url` | mTLS agent API if not `<host of --server>:8443`. |
| `--force` | Replace an existing enrollment or an `agent.toml` that points to another server. Without the flag the script never sends the token to a server other than the one of the existing configuration (T58). |
| `--repo` | Install the packages from the server's signed repository (apt, dnf, apk); the package manager updates them from then on. Needs a certificate the package manager trusts. |
| `--grant-log-access` | Alpine without journald: busybox syslogd writes with the group `sermon` instead of `wheel` (T70). |
| `--no-helper` | Install no helper, or switch an existing one off. |
| `--no-remote-updates` | `allow_remote_updates = false` |
| `--allow-remote-reboot` | `allow_remote_reboot = true` |
| `--no-self-update` | `allow_self_update = false` |
| `--no-smart` | `allow_smart = false` |

Without flags of the helper an existing `helper.toml` stays as it is. The script installs only binaries signed with
the embedded release key and for exactly the server's version (T48). It remains `curl | sh`: whoever takes over the
server can serve another script at the **next** installation (T50). "View the script first" in the UI and the package
repositories (`--repo`), whose keys the package manager holds, help against that. Package repositories and their
keys: [installation](../installation.md) (in German).

### systemd and OpenRC

The unit `sermon-agent.service` is hardened; `systemd-analyze security sermon-agent` is to stay below 2.0
(KONZEPT §18.4). Among others it sets:

- `User=sermon`, empty `CapabilityBoundingSet=` and `AmbientCapabilities=`, `NoNewPrivileges=yes`;
- `ProtectSystem=strict`, `ProtectHome=read-only` (the file system collector sees `/home`), `PrivateTmp=yes`,
  `PrivateDevices=yes`, `ReadWritePaths=/var/lib/sermon-agent`, `ReadOnlyPaths=/etc/sermon`;
- `RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK`, `SystemCallFilter=@system-service` without
  `@privileged @resources` (only `adjtimex` and `clock_adjtime` to read the clock), `MemoryDenyWriteExecute=yes`;
- limits `MemoryMax=128M`, `CPUQuota=20%`, `TasksMax=64` and `LimitCORE=0` (no core dumps: the memory holds the
  modules' credentials).

The helper (`sermon-helper.service`) starts through `sermon-helper.socket` on the first request and ends after a
minute without one. It runs as root with `ProtectSystem=full`, `ProtectHome=yes`, `NoNewPrivileges=yes`, devices
read-only for the disks only (`DeviceAllow=block-sd r`, `block-blkext r`), `CPUQuota=50%`, `MemoryMax=1G`. A run
(updates, reboot, self-update) is a unit of its own `sermon-helper-run-<id>` with the rights a package manager needs.

Change the units only to restrict them further (`systemctl edit sermon-agent`). Under OpenRC `supervise-daemon`
starts the agent as `sermon` without core dumps (`rc_ulimit="-c 0"`); systemd's sandbox is missing there, the
separation rests on user and file permissions.

## On the server

### Networks and ports

The compose ([`deploy/compose`](../../../deploy/compose/), [ADR 0028](../../adr/0028-produktions-compose.md))
separates two networks:

| Network | Services | Purpose |
|---|---|---|
| `edge` | `caddy`, `server` | published ports and outgoing connections (ACME, SMTP, channels, OIDC) |
| `internal` | `server`, `postgres`, `loki`, `migrate`, `backup`, `restore` | no route outside (`internal: true`) |

- **80/443** go to Caddy (TLS by ACME, reverse proxy to `server:8080`, admin API off). Only Caddy's fixed address
  (`SERMON_PROXY_IP`, default `172.30.78.10`) is in `SERMON_TRUSTED_PROXIES`.
- **8443** (agent API, mTLS 1.3) is published by Docker directly at the server, not through Caddy: so the server sees
  the agents' real addresses, which it needs to detect copied keys (T4).
- PostgreSQL and Loki have no published port. Loki has no sign-in of its own – only the server reaches it and names
  the organization in every request (T35).
- Every container is `read_only`, without capabilities (exceptions: Caddy binds 80/443, PostgreSQL switches its user)
  and without new privileges; no container gets the Docker socket (T73, `just lint-compose`).
- **Firewall:** Docker publishes ports past host firewalls such as `ufw`. To restrict the agent port to your hosts'
  networks, do it in the chain `DOCKER-USER` or in the provider's firewall.

### Behind another reverse proxy

If the host runs a reverse proxy already (Traefik, nginx …), it can take over TLS for the web UI instead of Caddy. The
rest of the compose stays as it is (network `internal`, `migrate`, `backup`, `restore`). What matters:

- **HTTP/2 to the server:** the web listener (`SERMON_HTTP_ADDR`, `:8080`) accepts HTTP/1.1 and HTTP/2 without TLS
  (h2c). The agents' enrollment is gRPC over the same listener – the proxy passes it on with HTTP/2. With Traefik,
  `scheme=h2c` for the whole service is enough; the UI speaks Connect over `fetch`, which works over h2c as well.
- **Agent port past the proxy:** Docker keeps publishing 8443 directly at the server. Through a proxy (even as TCP
  passthrough) the server would see the proxy's address instead of the agents' (T4).
- **Only the proxy sets `X-Forwarded-For`:** `SERMON_TRUSTED_PROXIES` holds just its address as `/32`. A network
  shared by all of the proxy's apps is too wide for that: every container on it reaches the server directly and picks
  its own address for rate limits and the audit log. Better a network of its own for proxy and server, with a fixed
  subnet and a fixed address of the proxy – like `edge` in the compose; the server then is not on the proxy's network.
- **Own CA:** at enrollment the agent checks the certificate against the system's CAs. With a certificate from an own
  CA, either install its root on the hosts or use `install.sh --ca-fingerprint` with the fingerprint of a CA the
  proxy sends along with the certificate (usually the intermediate,
  `openssl x509 -in intermediate.crt -noout -fingerprint -sha256`); the agent then checks chain and name against it.
  `install.sh --repo` needs the CA in the system. Afterwards the agent only talks to the agent port and SerMon's agent
  CA.
- `SERMON_ALLOWED_ORIGINS` is the address of the UI at the proxy, `SERMON_AGENT_HOSTNAMES` the name the agents reach
  port 8443 under.

With Traefik (Docker provider; network `sermon_edge` with `172.30.78.0/24`, Traefik fixed at `172.30.78.10` in it, so
`SERMON_TRUSTED_PROXIES=172.30.78.10/32`; checked with Traefik 3.8, including the enrollment with `--ca-fingerprint` of the intermediate):

```yaml
services:
  server:
    networks: [sermon_edge, internal]
    labels:
      - traefik.enable=true
      - traefik.docker.network=sermon_edge
      - traefik.http.routers.sermon.rule=Host(`sermon.example.org`)
      - traefik.http.routers.sermon.entrypoints=websecure
      - traefik.http.services.sermon.loadbalancer.server.port=8080
      - traefik.http.services.sermon.loadbalancer.server.scheme=h2c
```

### Variables that affect security

All variables with their defaults are in the [reference](configuration.md). Decisive for security:

- `SERMON_MODE` never `development` – invitations go out without mail and over `http://` there.
- `SERMON_ALLOWED_ORIGINS` exactly the address of the UI (`https://…`); it protects against CSRF.
- `SERMON_TRUSTED_PROXIES` only your own reverse proxy. Set too wide, every client picks its own address for rate
  limits and the audit log.
- `SERMON_OUTBOUND_ALLOWLIST` empty, or only the internal networks channels really need (e.g. an internal Mattermost or
  the OIDC provider). Everything else is SSRF surface (T43).
- `SERMON_MASTER_KEYS_FILE` opens every sealed value (CA keys, TOTP seeds, credentials; A3). The file is `0600` in the
  volume `sermon_server-state`; back it up apart from the backups.
- `SERMON_METRICS_ADDR` is off by default. The endpoint has no sign-in, only instance values – bind it to loopback or
  an internal network only ([operations](operations.md#self-monitoring)).
- `SERMON_AGENT_HOSTNAMES` only the names agents reach the server under.

### Secrets and file permissions

| What | Where | Permissions and notes |
|---|---|---|
| Database passwords and URLs | `deploy/compose/secrets/` (`secrets.sh`) | directory `0700`, files `0644` (the containers read them as their own users through bind mounts – the directory is what protects them). `secrets.sh` never overwrites. Only the database container needs `postgres_password` (superuser), only `migrate` and `restore` need `database_migrate_url`, only `backup` needs `database_backup_url`. |
| SMTP password, OIDC client secret | `secrets/smtp_password`, `secrets/oidc_client_secret` | without a line break |
| Master keys | `/var/lib/sermon/master_keys` in the volume `sermon_server-state` | `0600`; never in git, back up separately |
| Public backup keys | `secrets/backup_recipients` | not secret |
| Secret backup key, saved master keys | `secrets/backup_identity`, `secrets/master_keys` | create **only for a restore** and delete afterwards ([backup](../backup.md)) |
| Settings | `deploy/compose/.env` | no secrets |

Whoever is root on the server host has everything. Keep the host small (only Docker and SerMon), updated, with SSH by
key only – and monitor it with an agent itself.

### Sign-in and accounts

- **Superadmins** manage the instance but see no data of the organizations. Give them a second factor, a passkey
  preferably, and set up at least two, so that a lost factor doesn't lock the instance out. Keep recovery codes
  offline.
- **Organizations** can make two-factor sign-in mandatory (page "Organization"). Recommended for every organization
  with operators or admins: they start updates and reboots.
- **Passkeys** need a DNS name with a dot; under an IP address SerMon runs without them (`passkeys are off` in the log).
- **OIDC:** `SERMON_OIDC_MFA=trusted` only if the provider enforces a second factor itself. With JIT (`SERMON_OIDC_JIT`)
  the admin of an organization can take in every member of a mapped provider group; switch it off if that is not
  wanted. A provider on the internal network needs an entry in `SERMON_OUTBOUND_ALLOWLIST`.
- Sessions end after 30 minutes without activity, at the latest after 12 hours. From the fifth failure in a row an
  account is locked for 1, 2, 4 … up to 15 minutes. Sensitive actions ask for a renewed confirmation; it holds for
  15 minutes ([step-up](../../help/en/concepts/step-up.md)).

### Module credentials

The server keeps credentials sealed; they go to the agent only over the mTLS stream and stay there in memory only
(T109). Still, every node of the group gets them. Therefore:

- **Own monitoring users with few rights:** PostgreSQL with `pg_monitor` ([module](../../help/en/modules/postgres.md)),
  MySQL with `PROCESS` and `REPLICATION CLIENT` or MariaDB with `REPLICA MONITOR`
  ([module](../../help/en/modules/mysql.md)), Redis with an ACL user that may only run `INFO`
  ([module](../../help/en/modules/redis.md)), for Prometheus a token that only reads `/metrics`
  ([module](../../help/en/modules/prometheus.md)).
- **Sockets instead of ports** where possible: only the service creates the path, and PostgreSQL with `peer` needs no
  password at all. Over TCP outside loopback only with verified TLS (`verify`).
- Hosts with special credentials in **groups of their own**: credentials apply per group.
- The Docker socket only where needed ([module](../../help/en/modules/docker_socket.md)); the module `docker` reads the
  cgroups without a socket.

## What a compromised server can do – and what not

The central assumption: whoever takes over the server or an admin account must not become root on all nodes (A1).

**It can** (with the defaults, as far as the vetoes allow):

- have updates installed from the distribution's signed sources (`allow_remote_updates`) and bring the agent to a
  newer, signed version (`allow_self_update`, T40);
- request logs below `log_file_prefixes` and from the journal, start the live view (both redacted, T42, T81);
- run modules against the allowed targets and send credentials for them (T107, T109);
- have keys rotated, lock or deactivate nodes, redirect alerts and notifications (T67, T69);
- read all data of the instance, the credentials of channels and modules too: it holds the master keys and the role
  `sermon_app`. RLS protects against bugs of the application, not against a compromised process (T32, T33);
- serve another script at the next installation by `curl | sh` (T50).

**It cannot:**

- run arbitrary commands or scripts on a host – the protocol knows typed commands only, the helper fixed operations
  without free arguments (T12, T38, T40);
- have a foreign, unsigned or older agent installed (T40, T48);
- reboot a host while `allow_remote_reboot = false` holds (T90);
- change `agent.toml` or `helper.toml`, read the agent's keys or `/etc/sermon` (T81, T108);
- send modules to other networks, names or ports than allowed, use the Docker socket without `allow_docker_socket`
  (T107, T108);
- change the schema or apply migrations – only the service `migrate` knows the owner role (T73);
- decrypt saved backups: the secret backup key is not on the server (T116).

For hosts where even that is too much: `install.sh --no-helper` (no remote control), `allow_logs = false`,
`allow_live_view = false`, `allow_modules = false`, and agent updates by packages instead of self-update.
