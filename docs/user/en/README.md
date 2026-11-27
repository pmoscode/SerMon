# SerMon – User guide

This guide walks through working with SerMon: from the overview of the fleet to alerts, logs, updates and dashboards of
your own. It describes the workflows; the details of every page and every figure are in the help texts SerMon shows
itself (ⓘ next to a figure, `?` or the help button at the top for the page). The links below lead to exactly these
texts.

For operators of the instance there is the [admin documentation](../../admin/README.md) and the
[GDPR documentation template](../../privacy/en/README.md).

Deutsche Fassung: [Benutzerhandbuch](../de/README.md).

## Contents

1. [Signing in and your account](#1-signing-in-and-your-account)
2. [Roles and rights](#2-roles-and-rights)
3. [Fleet](#3-fleet)
4. [The node page](#4-the-node-page)
5. [Adding, deactivating and deleting nodes](#5-adding-deactivating-and-deleting-nodes)
6. [Alerts and silences](#6-alerts-and-silences)
7. [Notifications](#7-notifications)
8. [Logs and live view](#8-logs-and-live-view)
9. [Security dashboard](#9-security-dashboard)
10. [Updates and update policies](#10-updates-and-update-policies)
11. [Modules](#11-modules)
12. [Dashboards](#12-dashboards)
13. [Kiosk displays](#13-kiosk-displays)
14. [Comparison, heatmap, capacity and anomalies](#14-comparison-heatmap-capacity-and-anomalies)
15. [Managing the organization](#15-managing-the-organization)

## 1. Signing in and your account

You sign in with e-mail and password, with a passkey or – if the instance has an identity provider – via SSO. New
members join through an invitation by e-mail; the link works once within 72 hours.

In the account menu under **Account security** you set up two-factor authentication (authenticator app and recovery
codes), add passkeys and link your account to the identity provider. If your organization requires a second factor,
its pages are only reachable afterwards.

Sensitive actions – changing rights, starting updates, changing channels and many more – ask once more for a code,
passkey or password. The confirmation holds for 15 minutes.

Under **Account** in the account menu you change display name and e-mail address (the new address applies with the
code SerMon sends to it) and download all data SerMon holds about you. When your last membership ends, the account is
deleted after a grace period.

- [Account](../../help/en/pages/account.md)
- [Security (two-factor, passkeys, SSO)](../../help/en/pages/security.md)
- [Confirmation (step-up)](../../help/en/concepts/step-up.md)

You change the language (German, English) in the account menu; if you are a member of several organizations, you pick
the organization at the top of the page.

## 2. Roles and rights

Every member has roles on node groups: **viewers** look, **operators** act (acknowledge, silence, updates, reboots,
adding nodes), **admins** of a group manage its nodes, **admins of all groups** the whole organization. You don't see
the nodes of a group you have no role on – not in searches, dashboards or exports either. If a button is missing, the
role usually is; ask an admin of your organization.

- [Roles and rights](../../help/en/concepts/roles.md)
- [SSO groups](../../help/en/concepts/oidc-group-mappings.md)

## 3. Fleet

The start page **Fleet** lists all nodes you may see with their state (online means: contact within the last
60 seconds) and the key figures, as tiles or a table. The search (names and tags) and the filters by group and tag
narrow the list; a click opens the node. The tabs above the list lead to capacity, heatmap, comparison, anomalies and
dashboards. At the top, SerMon suggests modules when agents have found matching services.

- [Fleet](../../help/en/pages/fleet.md)
- [Tags](../../help/en/concepts/tags.md)
- Figures: [CPU](../../help/en/figures/cpu_busy.md), [memory](../../help/en/figures/memory_used.md),
  [fullest file system](../../help/en/figures/filesystem_max_used.md),
  [load per CPU](../../help/en/figures/load_per_cpu.md), [uptime](../../help/en/figures/uptime.md),
  [network](../../help/en/figures/network.md)

## 4. The node page

The node page shows everything about a server: host facts, charts by area (CPU, memory, disks, network), the file
systems with a forecast of when they are full, and tabs for processes (live), logs, security, updates, modules and
identity. All charts share the time range at the top; dragging zooms, a double click resets. Every chart has an ⓘ
explaining its metric and typical causes.

- [Node](../../help/en/pages/node.md)
- [Charts and time range](../../help/en/concepts/charts.md)
- [Live view of the processes](../../help/en/concepts/live-view.md) and
  [processes without details](../../help/en/concepts/process-na.md)
- [Identity of the node](../../help/en/concepts/node-identity.md)
- [Source of the security events](../../help/en/concepts/security-source.md) and
  [unverified source](../../help/en/concepts/unverified-source.md)

## 5. Adding, deactivating and deleting nodes

**Add node** (operators) creates an installation command with an enrollment token for a group. Run as root on the
server, it installs the agent, checks its signature and enrolls the node; seconds later it shows up in the fleet.

Admins of a group deactivate a node (it sends nothing anymore, its data stays) or delete it (its data is deleted after
a grace period, until then the deletion can be cancelled).

- [Add node](../../help/en/pages/add_node.md)
- [Deactivating and deleting](../../help/en/concepts/node-lifecycle.md)

## 6. Alerts and silences

The **Alerts** page shows what fires right now, with severity, node, rule and history. Every alert explains its cause
and what to do – the help text of its rule. **Acknowledge** means: someone takes care of it; repeats and escalation
stop. **Silence** opens a silence for the rule on the node; for planned work you create a silence with a comment and an
end (up to 30 days). Silences only hold back notifications; the alert stays visible.

The **Rules** tab lists the shipped rules with their thresholds; admins turn them off, adjust them or import rules of
their own (YAML). The **Statistics** tab shows which rules fire most and how fast people react.

- [Alerts](../../help/en/pages/alerts.md)
- [Alert statistics](../../help/en/concepts/alert-statistics.md), [firings](../../help/en/figures/alert_firings.md),
  [MTTA](../../help/en/figures/mtta.md), [MTTR](../../help/en/figures/mttr.md)

## 7. Notifications

Under **Notifications** admins create channels (e-mail, ntfy, Slack, Teams, Discord, Mattermost, Telegram, Gotify,
Pushover, webhook) and use policies to decide which alerts go where, with grouping, repeats and escalation. Maintenance
windows mute "node offline" and "reboot" every week at the same time. The log shows every delivery of the last
30 days.

- [Notifications](../../help/en/pages/notifications.md)
- Channels: [e-mail](../../help/en/channels/email.md), [ntfy](../../help/en/channels/ntfy.md),
  [Slack](../../help/en/channels/slack.md), [Teams](../../help/en/channels/teams.md),
  [Discord](../../help/en/channels/discord.md), [Mattermost](../../help/en/channels/mattermost.md),
  [Telegram](../../help/en/channels/telegram.md), [Gotify](../../help/en/channels/gotify.md),
  [Pushover](../../help/en/channels/pushover.md), [webhook](../../help/en/channels/webhook.md)

## 8. Logs and live view

The **Logs** page searches the nodes' log lines from the systemd journal and from log files: filters by node, group,
source, units, severity and text (no regular expressions), a histogram over the time range, the context around a line
and **Live** for new lines as they arrive. All filters are part of the address; a link shows others the same view as
far as they may see the nodes. What an agent sends and which secrets it masks beforehand is set by the organization's
admins, as is whether viewers see logs.

- [Logs](../../help/en/pages/logs.md)
- [Log settings and redaction](../../help/en/concepts/log_settings.md)
- [Retention](../../help/en/concepts/retention.md)

## 9. Security dashboard

The **Security** page sums up the sign-ins on the nodes: failed and successful SSH logins, root logins, sudo failures,
logins from new sources, plus top IPs and top users. Filters and time range apply to the whole page; the list can be
exported as CSV. Shipped rules report, for example, an SSH brute force or a root login.

- [Security](../../help/en/pages/security_events.md)
- [Failed logins](../../help/en/figures/failed_logins.md),
  [new sources](../../help/en/figures/new_source_logins.md), [root logins](../../help/en/figures/root_logins.md),
  [sudo failures](../../help/en/figures/sudo_failures.md)

## 10. Updates and update policies

The **Updates** page shows which nodes have pending updates (security updates first) and whether a reboot is due.
Operators start updates and reboots for single nodes or whole groups as a rollout; admins set an update policy per
group – report only, security updates or all updates in a weekly window – and can pause every update of the
organization. Agent updates take the same path.

- [Updates](../../help/en/pages/updates.md)
- [Updates of a node](../../help/en/concepts/updates.md)
- [Update policies](../../help/en/concepts/update-policy.md)

## 11. Modules

Modules monitor services of a host: nginx, Apache, PostgreSQL, MySQL/MariaDB, Redis, Docker, systemd units, TLS
certificates, HTTP and TCP checks, SMART and Prometheus exporters. They are built into the agent and off by default.
When an agent finds a service, SerMon suggests the matching module; admins turn it on per group and enter credentials
(stored encrypted, never shown again). What a module may reach is set by the operator on the host. The **Modules** tab
of the node page shows how every module is doing.

- [Modules](../../help/en/concepts/modules.md)
- Single modules: [nginx](../../help/en/modules/nginx.md), [Apache](../../help/en/modules/apache.md),
  [PostgreSQL](../../help/en/modules/postgres.md), [MySQL](../../help/en/modules/mysql.md),
  [Redis](../../help/en/modules/redis.md), [Docker (cgroups)](../../help/en/modules/docker.md),
  [Docker (socket)](../../help/en/modules/docker_socket.md),
  [systemd units](../../help/en/modules/systemd_units.md), [TLS](../../help/en/modules/tls.md),
  [HTTP check](../../help/en/modules/http_check.md), [TCP check](../../help/en/modules/tcp_check.md),
  [SMART](../../help/en/modules/smart.md), [Prometheus](../../help/en/modules/prometheus.md)

## 12. Dashboards

Under **Dashboards** you build views of your own from widgets: charts, single values, log lines and alerts in a grid.
With variables you pick a node or a group above the widgets, so one dashboard serves many nodes. A dashboard is yours;
shared, every member of the organization sees it – each with only the data their role allows. Dashboards can be
exported and imported as JSON.

- [Dashboards](../../help/en/pages/dashboards.md)
- [Editing a dashboard](../../help/en/pages/dashboard.md)

## 13. Kiosk displays

For a screen in the office, admins create a kiosk token on a shared dashboard. The address `/kiosk#t=…` shows the
dashboard without signing in, full screen and read-only – with only what both the admin who created the token and the
dashboard's owner may see. A token expires after 1 to 365 days and can be revoked at any time. The address is the
key: whoever has it sees the dashboard.

- [Kiosk displays](../../help/en/pages/dashboard.md) (section "Kiosk displays")

## 14. Comparison, heatmap, capacity and anomalies

- **Comparison** overlays two to four nodes in the same time range – before and after a change or within a cluster:
  [comparison](../../help/en/pages/compare.md).
- **Heatmap** shows all nodes over time, one row per node: [heatmap](../../help/en/pages/heatmap.md).
- **Capacity** lists file systems by their forecast and nodes that need more CPU and memory than a month ago:
  [capacity](../../help/en/pages/capacity.md).
- **Anomalies** shows nodes whose metrics deviate from their usual weekly pattern:
  [anomalies](../../help/en/pages/anomalies.md), [how the detection works](../../help/en/concepts/anomalies.md).

## 15. Managing the organization

Admins of all groups manage, under **Organization**, members and roles, invitations, node groups, required two-factor
authentication, SSO groups, log settings, modules, the retention of logs and events, and read the audit log. The
quotas are set by the operator of the instance, who as superadmin also manages the organizations themselves and,
under **Agent CA**, rolls the agents' certificate authority over in good time before it expires.

- [Organization](../../help/en/pages/admin.md)
- [Retention](../../help/en/concepts/retention.md)
- [Organizations of the instance (superadmin)](../../help/en/pages/organizations.md)
- [Agent CA and its rollover (superadmin)](../../help/en/pages/agent_ca.md)
