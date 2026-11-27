# Organization

Administration of the organization for admins of all groups: members and their roles, invitations, node groups, required two-factor authentication and the audit log.

## Roles

- **Viewers** see nodes, metrics and (if allowed) logs.
- **Operators** may also add nodes, acknowledge and silence alerts, trigger updates and reboots and unlock locked nodes.
- **Admins** of a group manage that group; admins of **all groups** the whole organization.

A role applies to one group or to all groups. Several roles add up. Details: [roles and rights](help:concepts/roles).

## Logs

The **Logs** tab sets what each group's agents send, whether viewers see logs and which redaction rules of your own apply; it also shows which settings each agent applied. More: [log settings](help:concepts/log_settings).

## Modules

The **Modules** tab turns on, per group, the modules its nodes' agents have, and sets them up. More: [Modules](help:concepts/modules).

## SSO groups

If the instance has an OIDC provider, the **SSO groups** tab maps groups of the provider to a role. At every sign-in these roles follow the groups; in the member list they carry the mark “SSO”. More: [SSO groups](help:concepts/oidc-group-mappings).

## Good to know

- Changing rights, removing members, deleting groups and inviting need a fresh confirmation (password or second factor).
- Invitations go by email to the invited address; only whoever has the mailbox can accept. The link works once within 72 hours. “Send again” sends a new link and invalidates the old one. Only on a development server without mail does SerMon show the link once to pass on. SerMon deletes expired invitations with their address; the audit log names only the invitation, not the address.
- A removed member without another organization loses the account after a grace period of the instance (default 30 days); if invited again before, the account stays.
- The last admin of all groups can't be removed.
- Every change is in the audit log with time and person; the entries are chained and can't be changed unnoticed.
- The **Quotas** tab shows the limits the superadmin of the instance set and how much of them is used. At a quota SerMon refuses new nodes and invitations; what exists keeps running.
