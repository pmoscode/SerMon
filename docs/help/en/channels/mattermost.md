# Mattermost

Notifications into a Mattermost channel through an incoming webhook.

## Setup

1. Under **Integrations** → **Incoming Webhooks** create a webhook for the channel (a system admin must have allowed it).
2. Enter the URL (`https://<server>/hooks/…`) here. SerMon stores it encrypted and never shows it again.

If Mattermost runs in the internal network, the operator of SerMon must allow its address in `SERMON_OUTBOUND_ALLOWLIST`.

## Appearance

The message has a coloured bar, the values of the alert and a link to the node in SerMon. Values of the servers are escaped; `@all`, `@channel` and `@here` in them notify nobody.
