# Dashboard

A custom dashboard: widgets in a grid of 12 columns. All charts follow the time range above; zooming into one zooms
all of them.

## Widgets

- **Time series** – a metric over the time range, one line per node and series.
- **Gauge** and **stat** – the latest value of a metric of one node.
- **Table** – the latest value of a metric per node and series.
- **Log stream** – the newest log lines, optionally only with a text or a severity.
- **Alert list** – the active alerts of the nodes.

Every widget reads with your rights: nodes you may not see stay empty. Live ranges refresh every 30 seconds; a widget
whose refresh failed keeps its last data and says that it is not up to date.

## Variables

A variable lets you pick a node or a group above the widgets; widgets that refer to it follow the choice. The choice
is part of the address, so a link opens the same view. Without a choice the default applies, else the first node or
group you see. Widgets with variables instead of fixed nodes also work after an import into another organization.

## Editing

- **Edit** opens the editor: add widgets, change them with the pencil, remove them with the cross.
- **Move:** drag the handle, or focus it with the Tab key and use the arrow keys. **Resize:** drag the corner, or use
  Shift and the arrow keys. Widgets in the way move down. Delete removes the focused widget.
- **Save** stores the dashboard. If someone changed it meanwhile, load it again and redo your change.
- **Share** shows the dashboard to the organization; **Export** saves it as JSON for an import elsewhere.

At most 30 widgets, 20 nodes per widget and 4 variables.

## Kiosk displays

Admins of all groups create a **kiosk token** at a shared dashboard (*Kiosk displays*, with a fresh confirmation of
their identity). The address it gives – `/kiosk#t=…` – shows the dashboard on a display without signing in: full
screen, without controls, read-only. It shows only what both the admin who created the token **and** the dashboard's
owner may see – also after the owner changed it; log widgets show the lines of the last day. The page refreshes every 30 seconds,
reconnects by itself after an outage and says since when its data is not current. Several tokens separated by commas
(`#t=…,…`) take turns, every 60 seconds or as set with `&rotate=` (seconds). For a real full screen start the
browser in its kiosk mode (e.g. `chromium --kiosk`).

The address is the key: whoever has it sees the dashboard. A token expires after 1 to 365 days; revoking it, deleting
the dashboard or no longer sharing it ends it at once – the display then shows nothing. When the admin loses the role
for all groups or leaves the organization, their tokens are revoked; getting the role back needs new ones. When the
owner leaves the organization, the display shows nothing any more; their private dashboards are deleted.
