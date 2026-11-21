# Pushover

Push-Benachrichtigungen aufs Handy oder den Desktop über Pushover.

## Einrichten

1. Auf pushover.net unter **Your Applications** eine Anwendung anlegen, z. B. „SerMon“.
2. **Token:** das API-Token der Anwendung (30 Zeichen).
3. **User:** der User-Key vom Dashboard oder der Key einer Gruppe (30 Zeichen). SerMon speichert Token und Key verschlüsselt und zeigt sie nie wieder an.
4. **Geräte (optional):** Gerätenamen, durch Kommas getrennt, z. B. `phone,tablet`. Ohne Angabe geht die Nachricht an alle Geräte.

## Priorität

- Kritisch: 1 (hoch, auch in den Ruhezeiten)
- Warnung, Behoben, Flattern und Test: 0 (normal)
- Quittiert und wieder stabil: −1 (leise)

Die Notfall-Priorität (2), die bis zur Bestätigung wiederholt, nutzt SerMon nicht; Eskalationen kommen über die Notification-Policies. Die Nachricht ist reiner Text mit einem Link zum Node in SerMon. Pushover begrenzt die Zahl der Nachrichten pro Monat und Anwendung.
