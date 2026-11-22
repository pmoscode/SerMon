# Zeit-Offset

Die letzte Abweichung der Uhr, die der NTP-Dienst an den Kernel gemeldet hat.

## Einordnung

Synchronisierte Hosts liegen meist unter einer Millisekunde, über das Internet bei wenigen Millisekunden. Dauerhaft mehr als 100 ms deuten auf eine schlechte Zeitquelle oder ein überlastetes Netz hin.

Metrik: `sermon_time_offset_seconds`
