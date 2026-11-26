# Zertifikat gelesen

1, wenn Zertifikate gelesen (Datei) oder vom Endpunkt vorgezeigt wurden, sonst 0 – die Datei fehlt, ist kein PEM, oder der Endpunkt antwortet nicht.

## Einordnung

Vom Modul `tls`; Label `target` ist die konfigurierte Datei bzw. `host:port`. Dateien nur unter `module_file_prefixes`, Endpunkte nur unter `module_targets` des Hosts.

Metrik: `sermon_tls_up`
