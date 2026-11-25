# Updates

What the nodes could update, the latest rollouts and the update policies of the groups.

## Nodes

The buttons at the top filter by status: security updates, other updates, reboot needed, up to date, not available
(no root helper or listing failed) and no report yet. **Oldest security update** says since when the oldest open
security update is reported. A click on the node opens its **Updates** tab with the packages, the runs and "Update
now".

## Rollouts

A rollout installs the updates of a group – by its policy in a window, or by hand. Each node goes through "waiting",
"updating", "rebooting" and "health check" to "done"; otherwise it says why it failed or was skipped (offline, no root
helper, forbidden by the host, end of the window).

## Update policies

Every group has a policy; without one of its own it only reports. Admins change it here or as YAML, see
[Update policy](help:concepts/update-policy).
