# Slack

Benachrichtigungen in einen Slack-Kanal über einen Incoming Webhook.

## Einrichten

1. In Slack unter **Apps verwalten** eine App anlegen (oder eine vorhandene nehmen) und **Incoming Webhooks** aktivieren.
2. **Add New Webhook to Workspace** wählen und den Kanal bestimmen.
3. Die Webhook-URL (`https://hooks.slack.com/services/…`) hier eintragen. Sie enthält den Zugang zum Kanal: SerMon speichert sie verschlüsselt und zeigt sie nie wieder an.

## Darstellung

Die Nachricht hat eine farbige Leiste (rot kritisch, gelb Warnung, grün behoben), die Werte des Alerts und einen Button, der den Node in SerMon öffnet. Werte der Server (Host- und User-Namen) erscheinen als reiner Text: Sie können niemanden erwähnen und keine Links einschleusen.
