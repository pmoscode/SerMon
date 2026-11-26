# Modules

Modules monitor services of a host – web servers, databases, certificates, disks. They are built into the agent, off
by default, and run only once an admin turns them on for a node group.

## Turning them on

**Organization → Modules** lists per group the modules its nodes' agents have. **Set up** opens the module's form;
every change asks for the password again, is in the audit log and reaches the connected agents at once. The server
checks the input first, the agent has the last word: if it refuses a configuration, the node shows "Configuration
refused" with the reason.

## What the host allows

Where a module may connect is up to the host's `agent.toml` (`[local]`): by default loopback only
(`module_targets`), `localhost` as the only name (`module_hosts`), only the ports of the modules' services
(`module_ports`: 80, 443, 465, 636, 993, 995, 3306, 5432, 6379, 8080, 8443, 9090–9999), Unix sockets below
`module_sockets` (not in directories everyone may write to, such as `/run/lock`), files below `module_file_prefixes`.
With `allow_modules = false` no module runs. The server can't change these bounds; the reasons of a refusal name no
address or path the server didn't send itself.

## Credentials

Fields for passwords and tokens never show a value, only "set" or "not set". For databases, create a monitoring user
with few rights. Credentials are per group: every node of the group gets them while the module is on. If only some
hosts should know a password, give them a group of their own. The modules' configuration is shown only to admins who
may manage modules.

## Suggestions

The agent looks at what runs on the host (processes, listening ports, units) and suggests fitting modules – e.g.
"nginx detected – enable the module?" at the node and in the fleet. Nothing is turned on by itself.

## States

- **Running:** the last run succeeded.
- **Error** and **Timed out:** the service didn't answer as expected; the reason is shown.
- **Hung** and **Crashed:** a fault of the module; the agent and other modules keep running, the module starts again
  after a pause.
- **Configuration refused** and **Forbidden by the host:** the setting or the host's `agent.toml` keep it from
  running.
