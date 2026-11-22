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
