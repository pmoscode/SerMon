# Gotify

Push-Benachrichtigungen über einen selbst betriebenen Gotify-Server.

## Einrichten

1. In Gotify unter **Apps** eine Anwendung anlegen, z. B. „SerMon“.
2. **Server:** Adresse des Gotify-Servers, z. B. `https://gotify.example.org` (auch mit Pfad, z. B. `https://example.org/gotify`). Ein Server im internen Netz muss der Betreiber in `SERMON_OUTBOUND_ALLOWLIST` freigeben.
3. **Token:** das Token der Anwendung. Wer es kennt, kann Nachrichten senden: SerMon speichert es verschlüsselt und zeigt es nie wieder an.

## Priorität

- Kritisch: 8 (laut)
- Warnung, Flattern und Test: 5 (mit Ton)
- Behoben: 3 (leise)
- Quittiert und wieder stabil: 2 (leise)

Die Nachricht ist reiner Text; ein Tippen darauf öffnet den Node in SerMon.
