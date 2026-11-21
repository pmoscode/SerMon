# Slack

Notifications into a Slack channel through an incoming webhook.

## Setup

1. In Slack under **Manage apps** create an app (or take an existing one) and enable **Incoming Webhooks**.
2. Choose **Add New Webhook to Workspace** and pick the channel.
3. Enter the webhook URL (`https://hooks.slack.com/services/…`) here. It grants access to the channel: SerMon stores it encrypted and never shows it again.

## Appearance

The message has a coloured bar (red critical, yellow warning, green resolved), the values of the alert and a button that opens the node in SerMon. Values of the servers (host and user names) appear as plain text: they can't mention anybody or smuggle in links.
