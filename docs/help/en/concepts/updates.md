# Updates

What the node could update, from its agent's last report: security updates first, and whether it needs a reboot.
Operators of the group can update and reboot by hand.

## Where the list comes from

Every 6 hours, after each run of the package manager and on request (**List again**) the agent asks the root helper for
the updates (apt, dnf or apk). Names, versions and sources come from the host's package sources and are shown as text
only. Alpine has no security updates: there is only "all".

## Update now

1. **Update now** shows the packages that would be installed – the security updates only, or all.
2. With **Reboot afterwards if needed** the node reboots if it needs to and the host allows it.
3. After the confirmation (with a fresh sign-in) a rollout runs with the group's `maxParallel`. The node's reboot and
   offline alerts are muted meanwhile.

## What the host allows

In `/etc/sermon/helper.toml` the host decides itself: `allow_remote_updates` (on by default) and `allow_remote_reboot`
(off by default). The server can't change that; the buttons are then locked and say why. Without the root helper there
are no remote updates.

## Agent

The agent's version, how it was installed and its channel. If it was installed by `install.sh` and the channel offers a
newer version, **Update the agent** updates it (with a fresh sign-in). The root helper checks the signature, replaces
the binary and puts the old one back if the new one doesn't connect within 2 minutes. The host can forbid it with
`allow_self_update = false`.
