# Confirmation (step-up)

Before sensitive actions SerMon asks once more that it is really you at the device: with the code of the
authenticator app, a passkey, via the identity provider or – without a second factor – with the password. The
confirmation then holds for 15 minutes for all actions.

## When SerMon asks

- **Rights:** granting and revoking roles, removing members, inviting, deleting groups, mapping SSO groups.
- **Sign-in:** setting up or turning off a second factor, generating new recovery codes, adding and deleting passkeys,
  unlinking an SSO identity, changing required two-factor authentication.
- **What alerts:** importing, enabling, disabling or resetting alert rules, creating silences, changing channels,
  policies and maintenance windows, acknowledging alerts of security rules.
- **Nodes:** creating enrollment tokens, renewing keys, unlocking, allowing a takeover, setting tags, deactivating,
  deleting and cancelling a deletion.
- **Actions on the hosts:** starting or cancelling updates, reboots and agent updates, changing update policies,
  pausing updates, turning modules on and off.
- **Data:** changing log settings and redaction, who sees logs and command lines, changing retention, creating
  kiosk tokens.

What only takes access away or only concerns your own view – ending a silence, revoking a token, changing a dashboard
– needs no confirmation.

## Good to know

- If the confirmation goes via the identity provider, you sign in there afresh and then run the action again.
- Switching the organization keeps the confirmation but grants no rights.
- Five wrong codes or passwords lock the account for a while.
- More on the factors: [Security](help:pages/security).
