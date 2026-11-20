# Organization

Administration of the organization for admins of all groups: members and their roles, invitations, node groups, required two-factor authentication and the audit log.

## Roles

- **Viewers** see nodes, metrics and (if allowed) logs.
- **Operators** may also add nodes and later acknowledge alerts and trigger updates and reboots.
- **Admins** of a group manage that group; admins of **all groups** the whole organization.

A role applies to one group or to all groups. Several roles add up.

## Good to know

- Changing rights, removing members, deleting groups and inviting need a fresh confirmation (password or second factor).
- Invitations are links shown only once; SerMon sends no emails.
- The last admin of all groups can't be removed.
- Every change is in the audit log with time and person; the entries are chained and can't be changed unnoticed.
