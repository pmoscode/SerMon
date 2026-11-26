# Restlaufzeit des Zertifikats

Zeit, bis das erste Zertifikat (das des Dienstes) abläuft; negativ, wenn es abgelaufen ist. Rechtzeitig erneuern, etwa 21 Tage vorher.

## Einordnung

Vom Modul `tls`; Label `target` ist die konfigurierte Datei bzw. `host:port`. Dateien nur unter `module_file_prefixes`, Endpunkte nur unter `module_targets` des Hosts.

Metrik: `sermon_tls_cert_expires_in_seconds`
