# Failed logins

SSH logins with a wrong password or key, and attempts with an unknown user, in the range.

## Assessment

- On servers with an SSH port open to the internet, constant failed logins are normal: bots try lists of names and passwords.
- Conspicuous are spikes, one address on several nodes, or attempts on internal nodes that shouldn't be reachable from outside.

## Next steps

1. Switch off password logins (`PasswordAuthentication no`) and allow keys only.
2. Make SSH reachable from known networks only (firewall, VPN).
3. Block an address that tries on many nodes.
