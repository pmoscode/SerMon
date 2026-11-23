# Rejected log files

Files a file pattern matches that the agent may not read.

## Reading it

- The agent reads only below the directories of `log_file_prefixes` in its `agent.toml` (default `/var/log`). It never reads its state directory, `/etc/sermon`, `/proc`, `/sys` or `/dev` – not through a symlink either. So even a compromised server can't read the agent's keys.
- More than 64 matching files count here as well.
- Patterns whose fixed start is outside already (e.g. `/etc/*`) are not even searched; they are named in the agent's log.

## Next steps

1. Check the pattern: does it point at a symlink leading outside?
2. If the agent should really read there, add the directory to `log_file_prefixes` on the host and restart the agent.

Metric: `sermon_agent_log_files_rejected`
