# Template: data protection documentation for operating SerMon

> **A template, not legal advice.** This document describes which personal data SerMon processes and which
> safeguards the software brings – as implemented in the code (as of v1.0). It replaces neither a review by a data
> protection officer nor legal advice. Legal bases, periods and organizational measures depend on your use and are
> only suggested here. Parts in **[square brackets]** are to be filled in. The German version is the primary one.

Deutsche Fassung: [Vorlage zur DSGVO-Dokumentation](../de/README.md).

## Contents

1. [Roles: who is responsible?](#1-roles-who-is-responsible)
2. [Which personal data SerMon holds](#2-which-personal-data-sermon-holds)
3. [Record of processing activities (Art. 30 GDPR)](#3-record-of-processing-activities-art-30-gdpr)
4. [Technical and organizational measures (Art. 32 GDPR)](#4-technical-and-organizational-measures-art-32-gdpr)
5. [Deletion periods and deleting](#5-deletion-periods-and-deleting)
6. [Processing on behalf of tenants (Art. 28 GDPR)](#6-processing-on-behalf-of-tenants-art-28-gdpr)
7. [Rights of data subjects](#7-rights-of-data-subjects)
8. [Known limits](#8-known-limits)

## 1. Roles: who is responsible?

SerMon is multi-tenant: an instance (the **operator**, with superadmins) holds one or more **organizations**. Each
organization manages its members, nodes (monitored servers) and settings itself; without a membership of their own a
superadmin sees no data of an organization.

- **Own operation** (operator and the only organization are the same body): the operator is the controller.
- **Operation for others** (e.g. a provider runs the instance for customers, each customer is an organization): each
  organization is the controller of its servers' data; the operator processes it on its behalf (section 6). For the
  members' accounts and the instance audit log the operator may be a controller itself – settle this in the contract.

| Item | Entry |
|---|---|
| Controller (name, address) | [to fill in] |
| Representative | [to fill in] |
| Data protection officer | [to fill in] |
| Operator of the instance (if different) | [to fill in] |
| Address of the instance | [e.g. `https://sermon.example.org`] |
| Location of server, database and backups | [data centre, country] |

## 2. Which personal data SerMon holds

Data subjects are mainly **members** (people with an account in SerMon) and **people who sign in to the monitored
servers or whose actions appear in their logs** (administrators, users of the services on the hosts, attackers). Host
names and IP addresses may be personal data as well, e.g. for servers of individuals.

| Category | Data | Source | Stored in |
|---|---|---|---|
| Account | e-mail address, display name, password hash (Argon2id), time of the password change, failed attempts and lock, since when without membership; a requested new e-mail address with the code as hash only (one hour) | setup, invitation, SSO, member | database |
| Sign-in factors | TOTP seed (encrypted), recovery codes (hash), passkeys (public key, name, last use), SSO identity (issuer, subject, e-mail, last sign-in) | member, identity provider | database |
| Sessions | session ID, start, last activity, time of the last confirmation (no IP address, no user agent) | sign-in | database |
| Membership | organization, roles per group, origin of the role (e.g. SSO group) | admins, identity provider | database |
| Invitations | e-mail address, role, inviting person, expiry (token as hash only) | admins | database, e-mail |
| Audit log | time, acting person (ID), action, target, details (e.g. new IP of a node; from v1.0 no e-mail addresses); the instance audit log also has changes, export and deletion of accounts (by ID only) | every change | database |
| Nodes | host name, `machine-id`, operating system, kernel, hardware, tags, last IP address of the agent | agent | database |
| Metrics | measurements of the host and its services (CPU, memory, disks, network, modules); labels such as file system, network interface, unit or container name | agent | database (TimescaleDB) |
| Logs | log lines from the systemd journal and from allowed log files – with whatever they contain (user names, IP addresses, URLs, content); known secrets replaced by the agent before sending | agent | Loki |
| Security events | SSH sign-ins (user name, source IP or host, port, method, key type and fingerprint), sudo (user, command line redacted), created and deleted accounts, reboots, OOM kills, enrollments and address changes of agents (IP address); optionally the country of the IP from a local GeoIP file | agent | database |
| Known login sources | per node: user name, source, first and last sign-in, count | from the security events | database |
| Alerts | active alerts and history with the rule's fields – for security rules e.g. user name and IP address | alerting | database |
| Notifications | channels (recipients such as e-mail addresses or chat targets; credentials encrypted), delivery log with the message content | admins, alerting | database, external services |
| Live view of the processes | processes with user and command line (redacted, cut to 1 KiB, can be turned off for the organization) | agent, only while someone watches | **not stored** |
| Server logs | operational messages; IP address of rejected enrollments; no passwords, tokens or request contents | server | the operator's container log |

On the hosts the agent buffers measurements and events while the server is unreachable (events up to 7 days,
`/var/lib/sermon-agent`).

## 3. Record of processing activities (Art. 30 GDPR)

Suggested entries. Check and adapt legal basis, recipients and periods per activity. The measures are collected in
section 4, the periods in section 5.

### 3.1 User accounts and access management

| | |
|---|---|
| Purpose | sign-in, managing rights and tracing access to the monitoring data |
| Data subjects | members, invited people |
| Data | account, sign-in factors, sessions, membership, invitations (section 2) |
| Legal basis | [e.g. Art. 6(1)(b) GDPR (contract), or Art. 88 GDPR and national law for employees] |
| Recipients | admins of the organization; mail server for invitations; identity provider (SSO) if used |
| Third country | [no / mail server or identity provider: …] |
| Period | accounts until 30 days after the last membership ends (adjustable) or until a request; sessions 30 minutes without activity, at most 12 hours; invitations 72 hours, then deleted |

### 3.2 Monitoring the servers (metrics and host data)

| | |
|---|---|
| Purpose | monitor availability, performance and capacity of servers and services, detect failures |
| Data subjects | usually no people; for servers of individuals their owners |
| Data | nodes, metrics (section 2) |
| Legal basis | [e.g. Art. 6(1)(f) GDPR (legitimate interest in secure operation)] |
| Recipients | members with a role on the node group; kiosk displays of the organization |
| Period | 10-second values 24 hours, minute values 30 days, hourly values 1 year |

### 3.3 Collecting and evaluating logs

| | |
|---|---|
| Purpose | troubleshooting and tracing failures and security incidents |
| Data subjects | people appearing in the hosts' logs (administrators, users of the services, third parties) |
| Data | logs (section 2) |
| Legal basis | [e.g. Art. 6(1)(f) GDPR; Art. 32 GDPR] |
| Recipients | operators and admins of the group; viewers only if the organization allows it |
| Period | per organization 1 to 14 days (default 14) |
| Controls | per group: logs off, minimum severity, excluded units, log files; own redaction rules; on the host `allow_logs = false` and allowed directories |

### 3.4 Security events on the servers

| | |
|---|---|
| Purpose | detect attacks and abuse (e.g. SSH brute force, sign-in from a new source, root login), investigate incidents |
| Data subjects | people who sign in to the hosts or try to |
| Data | security events, known login sources, alerts with their fields (section 2) |
| Legal basis | [e.g. Art. 6(1)(f) GDPR; Art. 32 GDPR] |
| Recipients | members of the group (viewers only if allowed); recipients of the notifications (3.5) |
| Period | per organization 7 to 365 days (default 365); known sources 1 year after the last sign-in |

### 3.5 Alerting and notifications

| | |
|---|---|
| Purpose | inform the people in charge about failures and security events |
| Data subjects | recipients of the notifications; people whose user name or IP address appears in an alert |
| Data | alerts, channels, delivery log (section 2) |
| Legal basis | [e.g. Art. 6(1)(f) GDPR] |
| Recipients | the channels the organization sets up: mail server, chat and push services (Slack, Microsoft Teams, Discord, Mattermost, Telegram, Gotify, ntfy, Pushover) or own webhooks |
| Third country | [check per channel; several of these services process data in the USA] |
| Period | alert history like the security events (3.4); delivery log 30 days |

### 3.6 Audit log

| | |
|---|---|
| Purpose | proof of who changed what and when (rights, rules, channels, updates, deletions …) |
| Data subjects | members (by their ID only); invited e-mail addresses in older entries |
| Data | audit log (section 2); for the instance a separate audit log of the superadmins |
| Legal basis | [e.g. Art. 6(1)(c)/(f) GDPR, Art. 5(2) GDPR (accountability)] |
| Recipients | admins of the organization; superadmins for the instance log and the export of a locked organization |
| Period | 2 years (`SERMON_AUDIT_RETENTION`, 90 days to 10 years), at most until the organization is deleted |

### 3.7 Live view of the processes

| | |
|---|---|
| Purpose | acute troubleshooting on a host ("top") |
| Data subjects | users running processes; people in command lines |
| Data | process list with user and command line (redacted) |
| Recipients | members of the group; command lines only for those who may see logs, and only if the organization doesn't hide them |
| Period | not stored; the agent sends only while the view is open (at most 15 minutes at a time) |
| Controls | on the host `allow_live_view = false` |

## 4. Technical and organizational measures (Art. 32 GDPR)

The measures of the software are described here; the operator adds those of the operation (data centre, physical
access, staff, backups, patching the host).

### 4.1 Confidentiality

**Physical access** to rooms and systems: [measures of the data centre or operator].

**System access** (sign-in):

- Passwords only as Argon2id hashes; from the fifth failed attempt the account is locked for a while (growing up to
  15 minutes), likewise for wrong codes.
- Two-factor authentication with TOTP or passkeys (WebAuthn); can be required per organization. Sign-in via an
  identity provider (OIDC) possible.
- Sessions end after 30 minutes without activity and after 12 hours at the latest; cookies over HTTPS only, SameSite.
- Sensitive actions require a fresh confirmation (step-up) that holds for 15 minutes.

**Data access** (permissions):

- Roles viewer, operator and admin per node group or for the whole organization; every action is checked centrally,
  anything not explicitly allowed is denied.
- Logs, security events and command lines can be withheld from viewers per organization; command lines can be hidden
  entirely.
- Superadmins manage the instance but see no data of an organization without a membership.
- Kiosk displays only read, only one dashboard and only what both the creating admin and the owner may see; tokens
  expire (1 to 365 days) and can be revoked.

**Separation** of tenants:

- Every query is bound to one organization; the database enforces the separation as well with row-level security.
  The server connects with a role without special rights.
- Logs are kept in Loki separately per organization; Loki is reachable from the internal network only.
- Agents are assigned to an organization and a node only by their client certificate, never by data in the stream.

**Encryption and pseudonymization**:

- Web UI over HTTPS only (TLS 1.2+, HSTS); agents over mTLS with TLS 1.3 and an own CA.
- Credentials of channels and modules, TOTP seeds and the key of the agent CA are encrypted with AES-256-GCM (master
  key kept apart from the database); credentials are never shown again after saving.
- Enrollment, invitation, kiosk and session tokens only as hashes; recovery codes only as hashes.
- Data at rest in database and Loki: [disk encryption by the operator].

### 4.2 Integrity

- **Transfer control:** the UI loads nothing from foreign servers (no CDNs, fonts or analytics; CSP `'self'` only).
  Outgoing connections to channels only through a checked client (no internal addresses without the operator's
  allowlist, no redirect to `http`). No telemetry, neither from TimescaleDB nor from Loki.
- **Input control:** every change is in the audit log with time and person; the entries are chained (hash) and
  can't be changed unnoticed.
- **Data minimization at the source:** the agent replaces passwords, tokens and private keys in logs, command lines and
  sudo commands before they leave the host; the organization adds rules of its own. What a host may send (logs, log
  files, live view, module targets) is set locally by its administrator – that always wins.
- **Integrity of the software:** agent binaries and package repositories are signed; the installation script checks
  the signature. The agent's root helper knows fixed operations only, no free commands.

### 4.3 Availability and resilience

- Agents buffer while the server is down (events up to 7 days); ingest acknowledges only after writing.
- Self-monitoring of the instance (among others database size, dropped data) with a warning to superadmins.
- Containers read-only, without capabilities; database and Loki without a route to the outside.
- Backups: [procedure, encryption, retention, location of the backups and of the master keys; admin documentation
  "backup and restore"]. Deleted data remains in older backups until they expire.
- Restore: [regularly tested on …].

### 4.4 Review, assessment and evaluation

- Development: automated tests of tenant separation and rights (every API function with every role), security scans
  of dependencies and code in CI, fuzzing of the parsers, threat model (`docs/security/threat-model.md`).
- Operation: [apply updates within …; review roles and members every …; check the audit log …].

## 5. Deletion periods and deleting

| Data | Period | Adjustable |
|---|---|---|
| Logs | 1–14 days, default 14; Loki deletes after 14 days at the latest | per organization (admins); cap by the superadmin |
| Security events and alert history | 7–365 days, default 365 | per organization (admins); cap by the superadmin |
| Metrics | 10-s values 24 h, minute values 30 days, hourly values 1 year | no |
| Anomaly values | 30 days | no |
| Known login sources | 1 year after the last sign-in | no |
| Delivery log of notifications | 30 days | no |
| Update runs | 1 year | no |
| Sessions | 30 minutes without activity, at most 12 hours | no |
| Invitations | valid for 72 hours, then deleted with their address (hourly) | no |
| Code of a new e-mail address | 1 hour, at most 5 attempts | no |
| Audit logs (organizations and instance) | 2 years, default 730 days | operator: `SERMON_AUDIT_RETENTION`, 90 days to 10 years |
| Accounts without membership | default 30 days after the last membership ends (superadmins excepted) | operator: `SERMON_ACCOUNT_DELETION_GRACE`, at least 24 hours |
| Server container logs | [the operator's rotation; `deploy/compose`: 5 × 10 MB per container] | operator |

The server deletes events hourly; Loki deletes logs about once a day – logs can therefore stay visible up to a day
longer than set.

**Deleting a node** (admins of the group): the node is deactivated at once and, after a grace period (default 7 days,
at least 24 hours, `SERMON_NODE_DELETION_GRACE`), deleted with its metrics, anomalies, security events, alerts and
logs. What stays: the entries in the audit log, the delivery log until its period ends and silences naming the node.

**Deleting an organization** (superadmin): the organization is locked and, after a grace period (default 30 days, at
least 7 days, `SERMON_ORG_DELETION_GRACE`), deleted with logs, metrics, events, audit log, nodes and all other data;
accounts without a membership in another organization go along. Before that, the audit log of the locked
organization can be exported.

**Deleting an account:** the server deletes an account without membership by itself after the grace period (hourly);
on request a superadmin deletes it at once (Organizations → "Delete an account on request", with a fresh
confirmation). Sessions, password, second factor, recovery codes, passkeys, SSO links, a pending change of the address
and private dashboards go with the account. Only the account's ID (UUID) stays in the audit log; each organization of
the account and the instance audit log get an entry without address. Superadmins and the last admin for all groups of
an organization can't be deleted this way.

**Audit logs** lose entries older than the retention from the start of the chain; an `audit.prune` entry names the
last deleted entry, so the chain stays verifiable from there. The newest entry always stays.

## 6. Processing on behalf of tenants (Art. 28 GDPR)

If you run SerMon for others (section 1), each organization needs a data processing agreement. The points below show
what SerMon provides for it; they don't replace the agreement.

| Obligation | With SerMon | To settle by the operator |
|---|---|---|
| Processing only on instructions | the organization itself sets what is collected, sent and how long it is kept (log settings, redaction, modules, retention, channels) | document instructions outside the UI |
| Confidentiality | superadmins see no data without a membership; members' actions are in the audit log | staff commitments |
| TOMs | section 4 | operation, physical access, backups |
| Sub-processors | SerMon itself calls no foreign services except: mail server, identity provider and the channels the organization sets up itself | list: [hosting, mail server, …] |
| Supporting data subjects' rights | section 7 | contact, deadlines |
| Reporting breaches | audit log, instance audit log, self-monitoring | reporting path [within … hours] |
| Deletion or return at the end | deleting the organization with a grace period; export of the audit log, security events as CSV, dashboards as JSON, rules as YAML | return of further data (e.g. metrics) by agreement |
| Evidence and audits | audit log, this documentation, threat model | audit rights |
| Quotas | limits per organization for nodes, members and retention | – |

## 7. Rights of data subjects

- **Access (Art. 15):** members download all data of their account as JSON under **Account → My data** (account,
  sign-in methods without secrets, sessions, memberships and roles, own dashboards, own audit entries; nothing about
  others; with a fresh confirmation, recorded in the instance audit log). Security events can be filtered by user name
  and IP address and exported as CSV; logs can be searched by text.
- **Rectification (Art. 16):** members change display name and e-mail address themselves (Account, with a fresh
  confirmation; a new address applies only with the code sent to it, the old address is notified). Metrics, logs and
  events record what happened on the hosts and are not rectified.
- **Erasure (Art. 17):** accounts on request by a superadmin, otherwise through the periods (section 5), deleting a
  node or the organization. Single log lines or events can't be deleted selectively; a shorter retention applies to
  all.
- **Restriction and objection (Art. 18, 21):** turn logs off or filter them per group, own redaction rules for
  certain details, withhold logs and command lines from viewers.
- **Data portability (Art. 20):** usually not relevant for members; exports see section 6.

## 8. Known limits

For the documentation to be accurate, take these points into account in your own concept (as of v1.0):

- **Audit entries from versions before 1.0** may contain invited e-mail addresses (`member.invite`) and the address
  of an organization's first admin (`org.create`, instance audit log); they go only with the retention of the audit
  logs, not with the account. The chain can't be redacted afterwards without breaking it.
- **Removing members** ends the membership at once, the account only after the grace period (default 30 days): an
  account survives a change of organization this way. On request a superadmin deletes it at once.
- **IP addresses** are not shortened or pseudonymized after a period; they go with their events.
- **Notifications** of security rules contain user names and IP addresses and go to the services of the configured
  channels; their storage periods are set by each service.
- **Logs** contain what the programs on the hosts write. Redaction recognizes known secrets, not personal data in
  general; rules of your own and the filters per group are there for that.
