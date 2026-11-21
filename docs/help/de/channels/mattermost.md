# Mattermost

Benachrichtigungen in einen Mattermost-Kanal über einen Incoming Webhook.

## Einrichten

1. Unter **Integrationen** → **Eingehende Webhooks** einen Webhook für den Kanal anlegen (das muss ein Systemadmin erlaubt haben).
2. Die URL (`https://<server>/hooks/…`) hier eintragen. SerMon speichert sie verschlüsselt und zeigt sie nie wieder an.

Läuft Mattermost im internen Netz, muss der Betreiber von SerMon die Adresse in `SERMON_OUTBOUND_ALLOWLIST` freigeben.

## Darstellung

Die Nachricht hat eine farbige Leiste, die Werte des Alerts und einen Link zum Node in SerMon. Werte der Server werden escapt; `@all`, `@channel` und `@here` darin benachrichtigen niemanden.
