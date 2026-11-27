# Security

What happens at the logins of the nodes: failed and successful SSH logins, root logins, sudo failures and further security events, with filters and export as CSV.

## Reading this page

- The **time range** applies to everything on the page. Dragging in the chart zooms in; the events are kept for 90 days by default unless the organization sets another period ([retention](help:concepts/retention)).
- The **totals** at the top count the range. A click on "Show …" filters the table to exactly these events.
- The **chart** stacks the failed logins by node; the six nodes with the most get a colour of their own, the others share one. The legend names every colour with its count.
- **Top IPs and top users** are the addresses and user names with the most failed logins. A click filters the table to one. "(empty)" is an empty user name an attacker tried.
- The **table** shows the newest events first. "new" means: the user logged in on the node from this source for the first time. "unverified" means: the line comes from a log file any local user can write – more in [unverified source](help:concepts/unverified-source).
- "Logs" in a row opens the logs of the node 15 minutes before and after the event.
- **Export as CSV** saves the events of the filter, at most 10,000. Values a spreadsheet would read as a formula start with an apostrophe.

## Typical next steps

1. Click a conspicuous address among the top IPs and check on which nodes it tries.
2. For a login from a new source, ask the person whether it was theirs.
3. Open the logs around an event to see what happened in the session afterwards.
