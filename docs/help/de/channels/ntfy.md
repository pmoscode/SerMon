# ntfy

Push-Benachrichtigungen aufs Handy oder den Desktop über einen ntfy-Server, öffentlich (ntfy.sh) oder selbst betrieben.

## Einrichten

- **Server:** Adresse des ntfy-Servers, z. B. `https://ntfy.sh`. Ein Server im internen Netz muss der Betreiber in `SERMON_OUTBOUND_ALLOWLIST` freigeben.
- **Topic:** Name des Themas, 1 bis 64 Buchstaben, Ziffern, `-` und `_`. Auf einem öffentlichen Server liest jeder mit, der das Topic kennt: Wähle einen langen, zufälligen Namen. SerMon behandelt das Topic deshalb wie ein Passwort.
- **Token (optional):** Access-Token des Servers (`tk_…`), wenn das Topic geschützt ist. Empfohlen für jedes Topic mit Security-Alerts.

## Priorität

- Kritisch: 5 (dringend)
- Warnung: 4 (hoch)
- Behoben und Flattern: 3 (normal)
- Quittiert und wieder stabil: 2 (niedrig)

Ein Tippen auf die Nachricht öffnet den Node in SerMon.
