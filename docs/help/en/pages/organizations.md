# Organizations

The organizations of the instance, for superadmins only: name, number of members and nodes, approximate storage, status and quotas. Data of the organizations – nodes, metrics, alerts, members – a superadmin does not see here; that needs a membership.

## Creating

A name and the email of the first admin are enough. The first admin gets an invitation by mail and manages the organization from then on; the superadmin does not become a member. Names are unique regardless of case. Creating and editing need a fresh confirmation.

## Quotas

Instead of the instance's values an organization gets its own limits:

- **Nodes:** further enrollments are refused.
- **Members:** counts open invitations too; further invitations and accepting are refused.
- **Retention cap:** applies once organizations set their own retention.
- **Samples per second and node, series per node:** an agent above it is throttled, new series are dropped.

Empty means the instance default. A lowered quota deletes nothing: what exists keeps running, only new things are refused. The admins of the organization see the state under Organization → Quotas. New ingest limits apply from an agent's next connection.

## Storage

The storage is an estimate: the size of all metrics of the instance is shared by the organization's share of the series, that of the security events by its share of the events. Compressed data can't be assigned to one organization exactly.

## Lock and delete

**Lock or delete** offers the steps the status allows. Each asks for the organization's name, typed exactly, and a fresh confirmation; each is recorded in the audit log of the organization and of the instance.

- **Lock:** without a deadline. Members are signed out at once and cannot get back in, agents are disconnected, enrollment tokens and invitations stop working. Alerts and notifications rest. Certificates and data stay. A superadmin signed in to the organization is signed out as well.
- **Schedule deletion:** locks as above and deletes after the instance's grace period (default 30 days, `SERMON_ORG_DELETION_GRACE`, at least 7 days).
- **Lift lock:** everything starts again; a scheduled deletion is cancelled.
- **Delete now:** only for a locked organization, at the earliest 10 minutes after the lock (until then hosts with a clock ahead may still send log lines). Nodes, metrics, events, alerts, channels, the audit log and every account without another membership are deleted for good; superadmin accounts stay.
- **Download audit log:** only for a locked organization, as JSON lines with the hash chain. The first line explains how to recompute the chain; the UI says whether it is intact.

The organization's admins get an email when it is locked (with the deletion date), when the lock is lifted and 7 days before the deletion.

## Delete an account on request

When a person asks for their account to be deleted, the superadmin deletes it here: enter the address twice, then
confirm again. Sessions, password, second factor, passkeys and SSO links go at once, private dashboards as well. Each
organization of the account gets an entry in its audit log, the instance audit log the request; only the account's ID
stays there, no address. Accounts of superadmins and the last admin for all groups of an organization can't be
deleted this way – appoint another admin there first.

SerMon deletes accounts without a membership anyway after a grace period (default 30 days,
`SERMON_ACCOUNT_DELETION_GRACE`); more under [Account](help:pages/account).
