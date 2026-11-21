# Alerts

The active alerts of the nodes you may see: what fires, what waits for its duration and what ended recently – most urgent first.

## Reading this page

- **Critical** and **Warning** fire and notify the channels. **Pending** waits until the condition has held long enough (the rule's duration). **Resolved** ended recently and leaves the list once it stays quiet.
- **Muted** means a silence or maintenance window holds back the notifications; the alert itself goes on and stays visible here.
- **Flapping** alerts change too often; their notifications are throttled until they are stable again.
- Values of the hosts (host names, user names, addresses) are shown as text. User and address of security alerts appear only for members who may see the security events.
- **History** shows the changes of an alert, newest first; it is kept for a year.

## Acknowledging

Acknowledging says “someone is on it”: reminders and escalation of the alert stop until it ends or becomes critical, and the channels hear who acknowledged it. Operators of the node's group may acknowledge; security alerts ask for your password again, because acknowledging them stops their escalation.

## Typical next steps

1. Open the node to look at the charts around the time the alert fired.
2. Fix the cause or acknowledge the alert so nobody else is woken up.
3. For planned work, mute the alerts with a silence or a maintenance window.
