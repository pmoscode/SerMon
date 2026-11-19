# Fleet

The overview of all servers you may see in your node groups – with status and the most important key figures at a glance.

## Reading this page

- **Online** means the agent reported within the last 60 seconds. A server without contact shows as **offline**, even if it only lost its network.
- The key figures show the latest value of the past minute: [CPU busy](help:figures/cpu_busy), [memory](help:figures/memory_used), [fullest file system](help:figures/filesystem_max_used), [load per CPU](help:figures/load_per_cpu), [uptime](help:figures/uptime) and [network](help:figures/network).
- A missing key figure has not been sent by the agent yet, for example right after it started.

## Typical next steps

1. Check offline servers: is the `sermon-agent` service running, can the server be reached?
2. Open servers with unusual values and look at the history in the node detail. How the charts work is explained in [Charts and time range](help:concepts/charts).
3. Add new servers with “Add node”.
