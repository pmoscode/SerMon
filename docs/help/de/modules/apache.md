# Apache httpd

Worker, Verbindungen und Verkehr von Apache aus `server-status?auto` (`mod_status`). Das Modul ist ab Werk aus; der
Agent schlägt es vor, wenn er Apache findet (Prozess `apache2` oder `httpd`, die Unit, Port 80/443).

## Voraussetzungen

- **Debian, Ubuntu:** `mod_status` ist an und zeigt `/server-status` nur für localhost – nichts zu tun.
- **Rocky, Alma, Fedora (RHEL-Familie):** `mod_status` ist geladen, die Seite fehlt. Etwa in
  `/etc/httpd/conf.d/sermon-status.conf`:

  ```apache
  <Location "/server-status">
      SetHandler server-status
      Require local
  </Location>
  ```

  Danach `apachectl configtest` und `systemctl reload httpd`. `Require local` lässt nur Anfragen vom Host selbst zu.

Die Seite zeigt ohne `?auto` auch die aktuellen Anfragen und Clients; das Modul liest nur die Zähler und schickt
keine Anfragen, URLs oder Adressen weiter. Der Agent erreicht nur Ziele unter `module_targets` (ab Werk Loopback).

## Einstellungen

- **Status-URL:** ab Werk `http://127.0.0.1/server-status?auto` – das `?auto` gehört dazu.
- **Timeout**, für HTTPS **TLS prüfen** und **CA-Datei**.

## Metriken

- `sermon_apache_up`: Apache antwortet.
- `sermon_apache_workers_busy`, `sermon_apache_workers_idle`, `sermon_apache_workers_max`: Sind alle Plätze belegt,
  warten Anfragen – `MaxRequestWorkers` prüfen.
- `sermon_apache_processes`, `sermon_apache_connections` (nicht bei prefork).
- `sermon_apache_requests_per_second`, `sermon_apache_sent_bytes_per_second`.

## Wenn es nicht läuft

- „status 403/404 – is mod_status enabled there, for this host?“: Seite nicht eingerichtet oder nicht für localhost
  freigegeben.
- „an HTML page: add ?auto to the URL“: Der URL fehlt `?auto`.
