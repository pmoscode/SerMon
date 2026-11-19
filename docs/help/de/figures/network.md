# Netzwerk

Die empfangenen und gesendeten Daten pro Sekunde, summiert über die Netzwerkschnittstellen des Servers – ohne Loopback und ohne die virtuellen Schnittstellen von Containern und VMs (etwa `veth`, `docker`, `virbr`).

## Einordnung

- Was normal ist, hängt ganz vom Dienst ab; auffällig sind vor allem Sprünge gegenüber dem üblichen Verlauf.
- Ungewöhnlich viel ausgehender Verkehr kann auf Backups, Updates oder einen kompromittierten Dienst hindeuten.
- Erreicht der Wert die Geschwindigkeit der Schnittstelle, ist die Leitung ausgelastet.

## Nächste Schritte

1. Im Node-Detail prüfen, welche Schnittstelle betroffen ist, und Fehler- und Drop-Zähler ansehen.
2. Verbindungen und Prozesse mit viel Verkehr suchen, z. B. mit `ss -tunap`.
