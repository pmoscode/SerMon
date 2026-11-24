# Live view of the processes

The host's processes as in `top`, updated every 2 seconds. The agent sends them only while someone is watching; nothing of it is stored.

## Header

- **CPU:** the busy share of all CPUs since the last state. The first state doesn't have it yet (–).
- **Memory:** used is what is not available (total minus "available"); the cache doesn't count as used.
- **Swap**, **load** over 1, 5 and 15 minutes, **uptime** and the processes per state.

## List

- **CPU** is the share of one core: a process with several threads can go above 100 %, as in `top`.
- **MEM** and **RSS:** the memory the process occupies right now.
- A click on a column sorts, a second one turns the order around. Values that weren't readable ("n/a") always come last.
- The **filters** work in the browser: user, a part of the name (regardless of case) and a regular expression over name and command line. An invalid expression doesn't filter and is pointed out.
- The **tree view** puts the processes below their parent. With a filter, the parents of a match are shown dimmed.
- **Pause** holds the display still while the data keeps coming; **Resume** shows the newest state.
- Kernel threads are shown in brackets, such as `[kworker/0:1]`.

## Limits

- The agent sends at most 4,000 processes, the busiest first; the rest are only counted.
- Command lines are redacted like logs (secrets replaced) and cut to 1 KiB. Admins can hide them for the organization entirely (Administration → Logs). Whoever may not read the node's logs – viewers of an organization that denies them logs – sees no command lines either.
- The view only reads. SerMon doesn't end or start processes.
- If the host's `agent.toml` says `allow_live_view = false`, the agent refuses and gives the reason.
- If the agent is not connected to the server right now, this is shown above the list; the view continues as soon as it connects.
