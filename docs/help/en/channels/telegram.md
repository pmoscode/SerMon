# Telegram

Notifications through your own Telegram bot into a chat, a group or a channel.

## Setup

1. In Telegram talk to **@BotFather** and create a bot with `/newbot`.
2. **Token:** the token of the bot (`123456789:AA…`). Whoever knows it can write as the bot: SerMon stores it encrypted and never shows it again.
3. Add the bot to the group or channel (in a channel as an admin allowed to post messages). For a private chat send `/start` to the bot once.
4. **Chat:** the numeric chat ID (groups and channels start with `-100…`) or `@channelname` for a public channel. `https://api.telegram.org/bot<token>/getUpdates` shows the ID, for example, after somebody wrote in the chat.
5. **Topic (optional):** the ID of a topic if the group uses topics.

## Appearance

The title is bold, below are the values of the alert and the link to the node in SerMon. Values from the hosts (user names, host names) appear as text, without formatting, links or mentions. Acknowledged alerts and alerts that are stable again arrive without sound.
