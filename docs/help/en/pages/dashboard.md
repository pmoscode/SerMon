# Dashboard

A custom dashboard: widgets in a grid of 12 columns. All charts follow the time range above; zooming into one zooms
all of them.

## Widgets

- **Time series** – a metric over the time range, one line per node and series.
- **Gauge** and **stat** – the latest value of a metric of one node.
- **Table** – the latest value of a metric per node and series.
- **Log stream** – the newest log lines, optionally only with a text or a severity.
- **Alert list** – the active alerts of the nodes.

Every widget reads with your rights: nodes you may not see stay empty.

## Editing

- **Edit** opens the editor: add widgets, change them with the pencil, remove them with the cross.
- **Move:** drag the handle, or focus it with the Tab key and use the arrow keys. **Resize:** drag the corner, or use
  Shift and the arrow keys. Widgets in the way move down. Delete removes the focused widget.
- **Save** stores the dashboard. If someone changed it meanwhile, load it again and redo your change.
- **Share** shows the dashboard to the organization; **Export** saves it as JSON for an import elsewhere.

At most 30 widgets, 20 nodes per widget and 4 variables.
