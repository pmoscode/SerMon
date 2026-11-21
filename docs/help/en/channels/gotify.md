# Gotify

Push notifications through a self-hosted Gotify server.

## Setup

1. In Gotify under **Apps** create an application, e.g. “SerMon”.
2. **Server:** address of the Gotify server, e.g. `https://gotify.example.org` (also with a path, e.g. `https://example.org/gotify`). The operator must allow a server in the internal network in `SERMON_OUTBOUND_ALLOWLIST`.
3. **Token:** the token of the application. Whoever knows it can send messages: SerMon stores it encrypted and never shows it again.

## Priority

- Critical: 8 (loud)
- Warning, flapping and test: 5 (with sound)
- Resolved: 3 (quiet)
- Acknowledged and stable again: 2 (quiet)

The message is plain text; tapping it opens the node in SerMon.
