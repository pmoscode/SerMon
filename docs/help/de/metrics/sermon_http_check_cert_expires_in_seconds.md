# Restlaufzeit des Zertifikats

HTTPS: Zeit, bis das Zertifikat des konfigurierten Hosts abläuft; negativ, wenn es abgelaufen ist.

## Einordnung

Vom Modul `http_check`; Label `target` ist das konfigurierte Ziel (eine URL ohne Query bzw. `host:port`). Ziele nur unter dem lokalen Veto des Hosts (`module_targets`, `module_hosts`, `module_ports`).

Metrik: `sermon_http_check_cert_expires_in_seconds`
