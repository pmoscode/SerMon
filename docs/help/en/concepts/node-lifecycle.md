# Deactivate and delete

A node that should no longer be monitored is deactivated or deleted; admins of its group can do both after a fresh confirmation, and both are in the audit log.

## Deactivate

All certificates of the node are revoked, its connection ends at once, and the server refuses it. Metrics, events and logs stay visible; alerts rest, open alerts end, and there is no "node offline" alert. The overview shows it as "Deactivated".

The node comes back by **Allow takeover** (Identity tab, operator rights) and a new installation of the agent with a token of its group: it keeps its ID and history.

## Delete

The host name confirms it. The node is deactivated at once and deleted after the grace – 7 days by default, set by the operator – with its metrics, anomalies, security events, alerts and logs. Until then **Cancel deletion** lifts the deletion; the node stays deactivated. A takeover is not possible during the grace.

What stays: the entries in the audit log, the notification log until its own retention, and silences naming the node. Until it is deleted the node counts against the organization's quota and keeps its `machine-id`.
