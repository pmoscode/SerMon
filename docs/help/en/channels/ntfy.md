# ntfy

Push notifications to phone or desktop through an ntfy server, public (ntfy.sh) or self-hosted.

## Setup

- **Server:** address of the ntfy server, e.g. `https://ntfy.sh`. The operator must allow a server in the internal network in `SERMON_OUTBOUND_ALLOWLIST`.
- **Topic:** name of the topic, 1 to 64 letters, digits, `-` and `_`. On a public server everybody who knows the topic can read along: choose a long random name. SerMon therefore treats the topic like a password.
- **Token (optional):** access token of the server (`tk_…`) if the topic is protected. Recommended for every topic with security alerts.

## Priority

- Critical: 5 (urgent)
- Warning: 4 (high)
- Resolved and flapping: 3 (default)
- Acknowledged and stable again: 2 (low)

Tapping the message opens the node in SerMon.
