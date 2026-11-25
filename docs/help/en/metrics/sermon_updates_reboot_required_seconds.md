# Reboot required for

How long the node has needed a reboot, from the first report that asked for it; 0 without.

## Context

The default rule warns after a day and turns critical after 7 days. A reboot (by hand or by the update policy with
autoReboot) resets the value to 0 with the next report.

Metric: `sermon_updates_reboot_required_seconds`
