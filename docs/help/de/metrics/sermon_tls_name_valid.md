# Name passt

Nur Endpunkte: 1, wenn das Zertifikat für den Host (oder den eingestellten Servernamen) gilt, sonst 0. Ein Endpunkt per IP-Adresse braucht die IP im Zertifikat oder einen Servernamen.

## Einordnung

Vom Modul `tls`; Label `target` ist die konfigurierte Datei bzw. `host:port`. Dateien nur unter `module_file_prefixes`, Endpunkte nur unter `module_targets`, `module_hosts` und `module_ports` des Hosts.

Metrik: `sermon_tls_name_valid`
