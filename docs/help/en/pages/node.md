# Node

Everything about one server: a header with host facts and current key figures, below it charts by area (CPU, memory, disks, network, processes, logs, identity).

## Reading this page

- All charts show the same time range and follow the mouse pointer together. Dragging zooms, a double-click resets – more in [Charts and time range](help:concepts/charts).
- Every chart has an ⓘ explaining the metric.
- The file system table shows when a file system runs full at the current trend (linear regression over 24 hours). “Not foreseeable” means no growth or more than a year away.
- In the network area the rates can be shown in bit/s instead of bytes/s.
- A notice below the name appears if the node cannot deliver security events or they only come unverified from a log file – more in [Source of security events](help:concepts/security-source).
- The **Processes (live)** tab shows the processes as in `top`, updated every 2 seconds – sortable, with filters, as a tree and with details per process. It runs only while it is open; more in [Live view of the processes](help:concepts/live-view).
- The Identity tab shows whether the node is locked, its last address and an open takeover window; operators rotate, revoke and unlock the key there or allow the takeover after a reinstallation – more in [Node identity](help:concepts/node-identity).

## Typical next steps

1. Narrow down an unusual figure in the chart: choose the time range, zoom into the spike.
2. Read the typical causes in the help of the metric.
3. Clean up or enlarge a file system with a near forecast before it runs full.
