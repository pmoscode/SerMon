# Logs

The log lines of the nodes you may see – from the systemd journal and from log files – with filters, histogram, context and a live view. Logs are kept for up to 14 days, as the organization sets it ([retention](help:concepts/retention)).

## Filtering

- **Range:** 15 minutes to 14 days up to now. Dragging across a part of the histogram shows only that part; **Back to …** returns to the chosen range.
- **Node** and **group**, **source** (journal or file), **units** (systemd units or syslog identifiers, separated by commas) and **severity** (“warning and above” shows warnings and everything more severe).
- **Text** looks for part of the line exactly as typed, optionally ignoring case. There are deliberately no regular expressions or query languages.
- All filters are part of the page's address: a link shows others exactly this view, as far as they may see the nodes.

## The histogram

- The bars count the lines of the filter per step, about 100 over the range, from a range of 100 minutes on in whole minutes, from 2 days on in whole hours. The first and the last bar may reach a little beyond the range.
- Without a text or units the histogram comes from counts the server keeps while storing the lines – over 14 days too, in a moment. With a text or units the log store has to read every line of the range; over long ranges that takes too long, and the histogram asks to shorten the range. The lines themselves still load.

## Reading the list

- The newest lines are at the top. **Load older lines** fetches the next 200; the list shows at most 2,000 lines.
- Lines always appear as text. Colour and control codes of terminals are removed; invisible characters and characters that reverse the reading direction appear marked as a symbol or as `⟨U+202E⟩`. So a line can't fake anything.
- **truncated** means: the agent cut a very long line (16 KiB).
- A click on the time shows the further fields (process, PID, file …) and **Lines before and after**: up to 50 lines before and after the entry from the same source and unit.
- Passwords, tokens and similar secrets are replaced with `***` by the agent before a line leaves the host.

## Live

**Live** shows new lines of the filter as they arrive (after a few seconds), at the top of the list. **Pause** keeps the list still; new lines wait and appear with **Resume**. If more than 100 lines per second arrive, live shows only the newest and says so. Lines an agent delivers late after an outage are in the normal query, not necessarily in live.

## Why lines may be missing

- The node forbids sending logs in its `agent.toml` (`allow_logs = false`); the page names such nodes.
- The agent lost journal entries because the journal was cleaned up while it was stopped; the page names the node and the approximate number.
- The group's settings send only part of the logs (severity, excluded units) or none at all.
- Viewers see logs only if the organization allows it.

## Typical next steps

1. Jump from a chart or an alert straight to the time with **Logs**.
2. Drag across the peak in the histogram and filter for **error and above**.
3. Open the context of an entry to see what happened before.
