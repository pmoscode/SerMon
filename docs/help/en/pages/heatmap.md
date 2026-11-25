# Heatmap

All nodes over time at a glance: every row is a node, every column a stretch of time, the colour the value. Patterns
stand out – a nightly job on every host, a "noisy neighbour" that loads the system at the same time each day.

## Using it

- **Coloured by** CPU (share not idle), memory, IO (the node's busiest disk) or
  [anomaly rate](help:concepts/anomalies). All values are shares from 0 to 100 %.
- The range spans 10 minutes to 30 days. SerMon sums it up to at most 200 columns; a cell shows the highest mean of its
  stretch.
- **Legend:** dark means little, bright much. Empty cells have no data (the node was off or reported nothing).
- **Values:** point at a cell, or focus the heatmap with the Tab key and move with the arrow keys (Home, End, Page
  up/down). The line above names node, stretch and value.
- **Order:** by name, or the highest or mean value first.
- **As a table** shows per node the mean, the highest value and when it was – without colours, with a link to the node.

At most 500 nodes are shown.

## Typical next steps

1. A bright column across many nodes: a shared job or an event at that time – look at the logs of that range.
2. A bright row: examine a node under constant load on its page.
