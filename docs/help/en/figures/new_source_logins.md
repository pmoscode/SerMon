# Logins from new sources

Successful SSH logins where a user logged in on the node for the first time from this address (IPv6: from this /64 network) or this host.

## Assessment

- Sources are new after a move, from another network or with new colleagues – mostly harmless.
- A login from a new source may however be a stolen password or a copied key.
- In a node's first week SerMon learns the usual sources; the rule "SSH login from a new source" reports only afterwards. A source unused for a year counts as new again.

## Next steps

1. Ask the person whether the login was theirs (address, method, key).
2. If not: lock the account, replace the password or key, look in the logs at what happened.
