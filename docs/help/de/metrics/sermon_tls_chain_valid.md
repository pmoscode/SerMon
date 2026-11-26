# Kette gültig

1, wenn die Kette zu einer CA des Systems (oder der eingestellten CA-Datei) führt, sonst 0 – meist fehlt ein Zwischenzertifikat oder das Zertifikat ist selbst signiert. Ein abgelaufenes Zertifikat zählt hier nicht: Das zeigt seine Restlaufzeit.

## Einordnung

Vom Modul `tls`; Label `target` ist die konfigurierte Datei bzw. `host:port`. Dateien nur unter `module_file_prefixes`, Endpunkte nur unter `module_targets`, `module_hosts` und `module_ports` des Hosts.

Metrik: `sermon_tls_chain_valid`
