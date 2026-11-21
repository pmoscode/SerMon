# Telegram

Benachrichtigungen über einen eigenen Telegram-Bot in einen Chat, eine Gruppe oder einen Kanal.

## Einrichten

1. In Telegram mit **@BotFather** sprechen und mit `/newbot` einen Bot anlegen.
2. **Token:** das Token des Bots (`123456789:AA…`). Wer es kennt, kann im Namen des Bots schreiben: SerMon speichert es verschlüsselt und zeigt es nie wieder an.
3. Den Bot in die Gruppe oder den Kanal aufnehmen (in einem Kanal als Admin mit dem Recht, Nachrichten zu senden). Für einen Einzelchat dem Bot einmal `/start` schreiben.
4. **Chat:** die numerische Chat-ID (Gruppen und Kanäle beginnen mit `-100…`) oder bei einem öffentlichen Kanal `@kanalname`. Die ID zeigt z. B. `https://api.telegram.org/bot<Token>/getUpdates`, nachdem jemand im Chat geschrieben hat.
5. **Thema (optional):** die ID eines Themas, wenn die Gruppe Themen nutzt.

## Darstellung

Der Titel ist fett, darunter stehen die Werte des Alerts und der Link zum Node in SerMon. Werte aus den Hosts (User-Namen, Hostnamen) erscheinen als Text, ohne Formatierung, Links oder Erwähnungen. Quittierte und wieder stabile Alerts kommen ohne Ton.
