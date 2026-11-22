# Node identity

Every agent connects with its own key and certificate; here they can be renewed, revoked, unlocked and handed over to a reinstallation.

## Locked

If the same node connects twice at the same time from different addresses, or rotates its key from a new address, the server locks it as a precaution: someone may have copied the key. The lock reason names the cause.

- **Unlock** when the explanation is harmless (for example a cloned container that is gone again).
- **Revoke key** when the key may have been stolen: all certificates become invalid and the agent has to be installed with a new token. The agent connected right now gets no new key – it could be the thief.

## Rotate key

A connected agent gets a new key at once and the old one is revoked. If the node is not connected, its certificates are revoked and it has to be installed again.

## Takeover

A reinstalled host usually keeps its `machine-id`; a new installation is then refused, so nobody takes over somebody else's node. **Allow takeover** opens a window for an hour: the next installation with the same `machine-id` and a token of this node's group takes it over. It keeps its history, its old certificates become invalid.

All actions need operator rights in the group and a fresh confirmation of the password; they are recorded in the audit log.
