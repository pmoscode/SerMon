# Notifications

Where alerts are reported: channels, the policies that route alerts to them, maintenance windows and the delivery log. Only admins of the organization manage them, and changes ask for your password again.

## Channels

A channel is a target such as e-mail, a chat webhook or a push service. How to get the URL or token is explained in the help of each type (ⓘ next to the type). Credentials are write-only: SerMon stores them encrypted and never shows them again; when editing, an empty field keeps the stored value. **Send test** delivers a test message at once and shows the error if it fails (a few tests per minute).

## Policies

A policy decides which alerts go to which channels:

- **Which alerts:** severities, groups of the node, rule patterns and the rule's labels. Patterns use `*` for any characters, e.g. `security.*` or `system.cpu.*`; labels are `key=value` lines, e.g. `category=security`. Empty means all; every filled field must match.
- Every matching policy applies, but a channel hears of a change only once.
- **Grouping:** per node (several alerts of a node in one round become one message) or as a digest of all alerts of a round.
- **Repeat:** a firing alert nobody acknowledged is sent again after this time (0: never).
- **Escalation:** after the given minutes without acknowledgement, more channels receive the alert, and from then on everything about it, its end included. Acknowledging stops repeat and escalation.

Without any policy, all alerts go to all enabled channels. As soon as there is a policy – even a disabled one – only the policies apply.

## Maintenance windows

Every week on the chosen days, from the start time for the given duration in the chosen time zone, the alerts “node offline” and “reboot” of the chosen groups (none: all groups) are muted. Other alerts still notify. For one-off work use a silence on the alerts page.

## Log

Every delivery of the last 30 days with its status. Errors name the host of the target, never credentials.
