# Update policy

What a group does with its updates on its own: only report, or install the security updates or all updates in a weekly
window, with a reboot if wanted.

## Mode and window

- **report only:** updates come by hand only.
- **security updates automatically:** the security updates in the window. Alpine has no security updates; its nodes
  only report then.
- **all updates automatically:** all updates in the window.

The window begins on the chosen days at the local time of its time zone and lasts 15 minutes to 24 hours. After its end
no node starts; running ones finish.

## Rollout

- **At once:** how many nodes of the group update at the same time (1 to 50), by host name.
- **Reboot after updates:** only if the node needs it, the host allows it (`allow_remote_reboot`) and the window is
  open.
- **Health check:** within this time the node must be back online – after a reboot with a new connection – and a
  minute without critical alerts.
- **Stop at the first failure:** then no further node starts; the event "rollout stopped" arises.

Changing needs admin rights on all groups and a fresh sign-in; every change is in the audit log. The policy can be
exported and imported as YAML (`kind: UpdatePolicy`).

## Agent

- **Update the agent in the window:** agents installed by `install.sh` update themselves in the window to the newest
  version of the channel – before the packages, one after another like these. Installations by package are updated
  by the package manager.
- **Channel:** `stable` is the server's version, `beta` a newer one the server offers for testing.
- The root helper checks every new version against the release key and rolls it back if it doesn't connect within
  2 minutes.
