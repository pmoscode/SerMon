# Scored metrics

How many series of the node the anomaly detection scores right now (such as CPU modes, memory, disks, interfaces).

## Context

Up to 64 per node. A series that is missing has too little history for its hour of the week yet, or the node is still
learning.

Metric: `sermon_anomaly_series_scored`
