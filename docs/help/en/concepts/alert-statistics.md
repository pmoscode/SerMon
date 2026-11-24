# Alert statistics

How loud the alerts of the last 7 or 30 days were and how fast people reacted – on the nodes you may see.

## What is counted

- A **firing** begins when an alert turns warning or critical from a calm state (OK, pending, resolved). Going from warning to critical is no new firing.
- Firings that begin in the range count. One that began before is left out – also its acknowledgement and its end.
- **Still active** ones keep firing; **not acknowledged** ones nobody acknowledged, even if they have been resolved since.
- **Of them flapping** are firings while the alert flapped; a rule that flaps often is usually set too sharp.

## Loudest rules

The ten rules with the most firings, with the number of nodes. A rule that fires often and resolves quickly by itself needs a longer duration (`for`) or another threshold rather than more attention.
