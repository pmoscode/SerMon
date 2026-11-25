# Anomaly rate

Which share of the node's scored metrics is unusual right now (0 to 100 %), computed anew every minute.

## Context

A metric is unusual when it lies far outside what is usual at this hour of the week and far from the course of the last
hours at the same time – for three of five minutes. Normal is 0 % or short, small values. From 10 % (critical: 25 %) the
default rule warns. During its first 7 days SerMon learns the node; the rate stays 0 meanwhile.

Metric: `sermon_anomaly_rate_ratio`
