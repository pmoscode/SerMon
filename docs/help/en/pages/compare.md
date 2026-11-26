# Compare

Two to four nodes over the same time range: before and after a change, members of a cluster, or an odd host next to an
ordinary one.

## Using it

- **Choosing nodes:** "Add node" adds up to four nodes, the ✕ removes one. The selection is part of the page's address –
  a link shows others the same comparison (they only see nodes they have rights for). "Compare" on the node page leads
  here.
- **Side by side:** per metric one chart per node, all on the same scale, so the same height means the same value.
- **Overlaid:** per metric one chart with a line per node.
- **Time range:** applies to all charts. Dragging in a chart zooms all of them, a double click resets; the crosshair
  shows the same moment in every chart.
- **Metrics:** CPU busy (share not idle), memory used, load per CPU (1 min), the busiest disk and the network traffic of
  all interfaces together.
- **No data:** a node without values in the range (off, new or unreachable) is named above the charts. An unknown node in
  the address (deleted or of another organization) is not queried.

## Typical next steps

1. One node stands out: look at its details and the logs around that time on its page.
2. All nodes show the same spike: a shared cause (job, deployment, load from outside) – the
   [heatmap](help:pages/heatmap) shows whether the whole fleet was affected.
