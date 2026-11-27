# Account

Your own account: display name, the e-mail address you sign in with and the export of all data SerMon holds about you.
Every change and the export ask for your confirmation first.

## Name and e-mail address

- Other members see your **display name**, e.g. on acknowledgements and in the audit log.
- A new **e-mail address** applies only with the code SerMon sends to it: it is valid for one hour and five attempts.
  From then on you sign in with the new address; the old one gets a message about the change. If the address belongs
  to another account already, SerMon refuses it after the code.
- Without a mail server (development only) the new address applies at once.
- Signing in via SSO doesn't depend on the address but on your identity at the provider.

## My data

**Export data** downloads a JSON file: account, password (only since when), second factor and passkeys without
secrets, SSO links, sessions, memberships with roles, your dashboards and your entries in the audit log (time and
action). It contains no data of other people. The export is recorded in the instance audit log.

## When an account is deleted

- When your last membership ends, SerMon deletes the account after a grace period (default 30 days); if you are
  invited again before, it stays with its passkeys and second factor.
- On request, the operator of the instance deletes the account at once ([Organizations](help:pages/organizations)).
- Afterwards only an ID stays in the audit log, no name and no address. More on the confirmation:
  [Confirmation (step-up)](help:concepts/step-up).
