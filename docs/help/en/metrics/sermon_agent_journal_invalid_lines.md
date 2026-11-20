# Unreadable journal lines

Lines of `journalctl` the agent could not read as a journal entry since it started: no valid JSON, longer than 1 MiB
or without a position in the journal. The agent skips them; all other entries arrive.

## Reading it

Single lines are harmless. If the value keeps rising, `journalctl` on this system produces output the agent does not
understand – please report the systemd version then (`journalctl --version`).

Metric: `sermon_agent_journal_invalid_lines`
