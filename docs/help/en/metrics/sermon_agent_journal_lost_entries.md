# Lost journal entries

Entries of the systemd journal the agent could not read since it started because they were already deleted. The agent
notices this from gaps in the journal's sequence numbers.

## Reading it

- 0 is normal.
- Values above 0 mean security events and logs are missing for that time. This typically happens after the agent was
  stopped for a while and the journal was cleaned up meanwhile (`journalctl --vacuum-*`, `SystemMaxUse`).

## Next steps

1. Check whether the agent was stopped for a longer time.
2. Increase the size of the journal (`SystemMaxUse` in `journald.conf`) if it rotates very quickly.

Metric: `sermon_agent_journal_lost_entries`
