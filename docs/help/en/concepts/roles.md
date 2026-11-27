# Roles and rights

What viewers, operators and admins may do. A role applies to one node group or to all groups of the organization;
several roles add up, and without a role on a group you don't see its nodes.

## Viewer

- See the group's nodes, metrics, alerts, updates and dashboards.
- Read logs and security events – unless the organization denies them to viewers
  ([log settings](help:concepts/log_settings)).
- Open the [live view](help:concepts/live-view) of the processes; the full command lines only if the organization
  allows them.
- Create dashboards of your own; view shared dashboards. Every widget shows only what the viewer's role allows.

## Operator

Everything a viewer may, plus on the group:

- Acknowledge and silence alerts.
- Trigger updates and reboots.
- Add nodes (enrollment token), renew a node's keys, unlock locked nodes and allow a takeover
  ([identity](help:concepts/node-identity)).

## Admin of a group

Everything an operator may, plus set the nodes' tags ([tags](help:concepts/tags)) and deactivate and delete nodes
([lifecycle](help:concepts/node-lifecycle)).

## Admin of all groups

Manages the whole organization: members, invitations and groups, alert rules, channels, policies and maintenance
windows, [update policies](help:concepts/update-policy), modules, log settings and redaction,
[retention](help:concepts/retention), required two-factor authentication, other members' shared dashboards, kiosk
tokens and the audit log.

## Superadmin

The operator of the instance creates, locks and deletes organizations, sets their quotas, manages the agent CA and
deletes accounts on request.
Without a membership of their own they see no data of an organization.

## Good to know

- Granting and revoking rights, removing members and inviting require a [confirmation](help:concepts/step-up).
- Roles from SSO groups follow the groups at the provider on every sign-in
  ([SSO groups](help:concepts/oidc-group-mappings)).
- The last admin of all groups can't be removed.
