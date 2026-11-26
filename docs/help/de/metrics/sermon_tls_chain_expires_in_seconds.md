# Restlaufzeit der Kette

Zeit, bis das erste Zertifikat der Kette abläuft, Zwischenzertifikate eingeschlossen. Kleiner als die Restlaufzeit des Zertifikats, wenn ein Zwischenzertifikat früher abläuft.

## Einordnung

Vom Modul `tls`; Label `target` ist die konfigurierte Datei bzw. `host:port`. Dateien nur unter `module_file_prefixes`, Endpunkte nur unter `module_targets`, `module_hosts` und `module_ports` des Hosts.

Metrik: `sermon_tls_chain_expires_in_seconds`
