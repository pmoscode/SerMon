# Alerts

The active alerts of the nodes you may see: what fires, what waits for its duration and what ended recently – most urgent first.

## Reading this page

- **Critical** and **Warning** fire and notify the channels. **Pending** waits until the condition has held long enough (the rule's duration). **Resolved** ended recently and leaves the list once it stays quiet.
- **Muted** means a silence or maintenance window holds back the notifications; the alert itself goes on and stays visible here.
- **Flapping** alerts change too often; their notifications are throttled until they are stable again.
- Values of the hosts (host names, user names, addresses) are shown as text. User and address of security alerts appear only for members who may see the security events.
- **History** shows the changes of an alert, newest first; it is kept for a year.
- **Charts around the time** opens the node page an hour before to an hour after the alert fired, in the area of its rule (CPU, memory, disks, network, security); **Logs** shows the node's logs around the time, for an alert of a systemd unit only that unit's lines.

## Acknowledging

Acknowledging says “someone is on it”: reminders and escalation of the alert stop until it ends or becomes critical, and the channels hear who acknowledged it. Operators of the node's group may acknowledge; security alerts ask for your password again, because acknowledging them stops their escalation.

## Silences

A silence mutes the notifications of matching alerts for up to 30 days: groups, single nodes of those groups, rule patterns (`security.*`), labels or severities – it has to name groups, rules or labels, so nothing is muted by accident, and it needs a comment. **Mute** on an alert opens a silence for its rule on its node. The alerts stay visible; whatever still fires when the silence ends is sent once. Operators mute their own groups and confirm with their password; ending a silence early needs no password.

## Rules

The rules decide when an alert fires. Every member can read them with their help and YAML. Admins switch rules off, adapt them, add their own or import several at once:

- Rules are typed YAML documents (threshold, rate, state, absence, prediction, event, anomaly) – no free queries. `anomaly` compares a node's anomaly rate (0 to 1) over a window and stays silent while the node learns. The editor checks every document; **Check** shows problems with document and line, and the changes as a diff, without storing anything. **Save** stores all documents or none.
- A changed default rule can be compared with the shipped one and reset; an own rule can be deleted.
- **Export** gives the changed and own rules (or all) as one YAML file, e.g. for another organization. Whether a rule is switched off is not part of the file.
- Changes apply from the next evaluation (within 10 seconds) and ask for your password again.

## Statistics

The **Statistics** tab shows for the last 7 or 30 days how often alerts fired, the loudest rules and how long it took on average until the acknowledgement (MTTA) and the resolution (MTTR) – more in [Alert statistics](help:concepts/alert-statistics).

## Typical next steps

1. Open the node to look at the charts around the time the alert fired.
2. Fix the cause or acknowledge the alert so nobody else is woken up.
3. For planned work, mute the alerts with a silence or a maintenance window.
