# PostgreSQL erreichbar

1, wenn der Server die Verbindung angenommen und geantwortet hat, 0, wenn nichts die Verbindung annimmt (gestoppt,
abgestürzt, falscher Port oder Socket).

## Einordnung

Vom Modul `postgres`. Eine abgelehnte Anmeldung oder fehlende Rechte zählen nicht als 0: Die nennt die Gesundheit des
Moduls mit dem Grund des Servers. Die Standardregel „PostgreSQL nicht erreichbar“ meldet 0 nach 2 Minuten.

Metrik: `sermon_postgres_up`
