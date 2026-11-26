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

**Cancel** stops a running rollout (the right to run updates on the group, with a fresh sign-in): waiting nodes are
skipped, runs the node hasn't begun are withdrawn; nodes updating right now finish without a reboot.

## Pause updates

Admins pause all updates of the organization here, for instance when an account may be taken over: no rollout starts
another node, waiting runs don't go to the nodes, updates, reboots and agent updates by hand are refused. Runs under
way go to their end. The notice at the top shows everybody since when; **Resume updates** lifts it. Both are in the
audit log.

## Update policies

Every group has a policy; without one of its own it only reports. Admins change it here or as YAML, see
[Update policy](help:concepts/update-policy).
