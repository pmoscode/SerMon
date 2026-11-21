# Pushover

Push notifications to phone or desktop through Pushover.

## Setup

1. On pushover.net under **Your Applications** create an application, e.g. “SerMon”.
2. **Token:** the API token of the application (30 characters).
3. **User:** the user key from the dashboard or the key of a group (30 characters). SerMon stores token and key encrypted and never shows them again.
4. **Devices (optional):** device names separated by commas, e.g. `phone,tablet`. Without, the message goes to all devices.

## Priority

- Critical: 1 (high, also during quiet hours)
- Warning, resolved, flapping and test: 0 (normal)
- Acknowledged and stable again: −1 (quiet)

SerMon does not use the emergency priority (2), which repeats until confirmed; escalations come with the notification policies. The message is plain text with a link to the node in SerMon. Pushover limits the number of messages per month and application.
