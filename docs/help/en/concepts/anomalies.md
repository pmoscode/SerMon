# Anomalies

SerMon detects unusual values without fixed thresholds: every minute it compares about 30 core metrics per node with
what was usual at the same hour of the week over the last four weeks, and with the course of the last hours.

## Score and band

- The **score** says how far a value lies outside what is usual, measured in robust standard deviations. From 3 it is
  notable, from 4 conspicuous.
- A minute is **anomalous** only when both comparisons exceed 4, for 3 of 5 minutes; single outliers don't count.
- The **red band** in the charts marks these minutes, and the title then says "unusual".
- **What was unusual?** lists the metrics with the highest score for a range. Mark the range by dragging in a chart;
  a double click resets it.
- What happens regularly, such as a nightly backup of a few minutes, counts as usual at its hour.

## Learning phase

New nodes and new metrics learn for 7 days before SerMon scores them: only then is there a basis for every hour of the
week. Until then the node's page says "still learning (day 3/7)", and there is neither a band nor an alert.

## Anomaly rate

The share of a node's scored metrics that are anomalous right now. The default rule warns from 10 % and turns critical
from 25 %.
