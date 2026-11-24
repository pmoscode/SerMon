# SSO groups

Groups of the identity provider give roles in your organization: whoever is in the group at the provider gets the role at the next sign-in over SSO – and loses it as soon as they are no longer in the group.

## How mappings work

- A mapping names the group exactly as in the provider's `groups` claim, a role and a node group or all groups. If several match, the highest role per node group applies.
- The roles are reconciled at every sign-in over the provider. Whoever stays signed in keeps the roles until the next sign-in; to take them away at once, do it in the member list.
- Roles an admin gave by hand or by invitation stay untouched. If you grant a role from a mapping again by hand, it is yours: the sign-in won't change it anymore.
- The last admin of all groups stays, even if the provider takes the group away.

## New members

If creating accounts is on, a person SerMon doesn't know yet gets an account at the sign-in and becomes a member when a mapping matches. This works only with a verified e-mail and only if no account has that e-mail; the organization's quota must have room. Whoever loses all roles stays a member without rights.
