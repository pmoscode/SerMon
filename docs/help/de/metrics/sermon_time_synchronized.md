# Zeit synchronisiert

1, wenn der Kernel die Uhr für synchronisiert hält (ein NTP-Dienst steuert sie).

## Einordnung

Normal ist 1. Bei 0 läuft kein Zeitabgleich (chrony, ntpd, systemd-timesyncd) oder er erreicht keinen Server; die Uhr driftet dann.

Metrik: `sermon_time_synchronized`
