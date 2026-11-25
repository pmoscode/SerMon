# Age of the oldest security update

Since when the node's oldest open security update is known; 0 without security updates.

## Context

The time counts from the report in which SerMon first saw the update. The default rule warns after 3 days and turns
critical after 14 days. Once the oldest is installed, the value drops to the age of the next one.

Metric: `sermon_updates_security_age_seconds`
