# nginx

Verbindungen und Anfragen von nginx aus seiner Statusseite (`stub_status`). Das Modul ist ab Werk aus; der Agent
schlägt es vor, wenn er nginx findet (Prozess, Unit oder Port 80/443).

## Voraussetzungen

nginx muss die Statusseite zeigen, nur für den Agent auf demselben Host – etwa in
`/etc/nginx/conf.d/sermon-status.conf`:

```nginx
server {
    listen 127.0.0.1:80;
    location = /nginx_status {
        stub_status;
        allow 127.0.0.1;
        deny all;
    }
}
```

Danach `nginx -t` und `systemctl reload nginx`. Der Block auf `127.0.0.1:80` nimmt nur Anfragen an 127.0.0.1 an, die
Seiten nach außen bleiben unberührt. Die Seite zeigt nur Zähler, keine Daten der Besucher. Läuft nginx auf einem
anderen Port, die URL im Modul anpassen. Der Agent erreicht nur Ziele unter `module_targets` (ab Werk Loopback).

## Einstellungen

- **Status-URL:** ab Werk `http://127.0.0.1/nginx_status`, ohne Query (`?…`); der Port muss unter `module_ports` in
  `agent.toml` liegen (ab Werk etwa 80, 443, 8080, 8443).
- **Timeout**, für HTTPS **TLS prüfen** und **CA-Datei**.

## Metriken

- `sermon_nginx_up`: nginx antwortet.
- `sermon_nginx_connections_active`, `…_reading`, `…_writing`, `…_waiting`: offene Verbindungen nach Zustand.
- `sermon_nginx_requests_per_second`, `sermon_nginx_connections_accepted_per_second`, `…_handled_per_second`.
- `sermon_nginx_connections_dropped_per_second`: abgewiesene Verbindungen – `worker_connections` erhöhen.

## Wenn es nicht läuft

- „status 404 – is stub_status enabled there?“: Die Seite ist nicht eingerichtet oder die URL falsch.
- „not a stub_status page“: Unter der URL antwortet etwas anderes.
- `sermon_nginx_up` 0: nginx antwortet nicht.
