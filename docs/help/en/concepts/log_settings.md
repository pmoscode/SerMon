# Log settings

What the agents send as logs, who may see them and what is redacted before. Admins for all groups change them; every change asks for the password again.

## Viewers

Logs and security events contain personal data (user names, addresses). With the switch, viewers no longer see them; operators and admins still do. It applies from the next request of every session; running live views end.

## Command lines in the live view

Command lines can contain secrets, even though the redaction replaces the known patterns. When hidden, the agents no longer send them and the server removes them as well. Open live views reconnect within 30 seconds and then show "hidden". The change is in the audit log.

## Per group

- **Send logs:** off means the group's agents send no logs. Security events still come.
- **Minimum severity** and **excluded units** (systemd units or syslog identifiers) save storage and noise.
- **Log files:** absolute patterns with `*` and `?`, e.g. `/var/log/nginx/*.log`. An entry starts with a line the regular expression matches (e.g. `^\d{4}-` for lines with a date); without one, indented lines belong to the entry before. The agents read only below the directories their host allows (`log_file_prefixes` in `agent.toml`, default `/var/log`) – never their own keys.

## Redaction

The agents replace passwords, tokens and private keys with `***` before a line leaves the host. Your own rules come on top: a regular expression replaces its match, or only the group `(?P<secret>…)`. **Test** shows on a sample line what the agents would send with the rules in the form.

## State per node

Which version of the settings each agent applied. An error means: the agent refused the settings (e.g. a regular expression it can't compile) and keeps working with the previous ones. "allow_logs = false" means: the host forbids sending locally, which always wins.
