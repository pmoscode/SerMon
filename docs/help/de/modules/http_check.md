# HTTP-Check

Prüft HTTP- und HTTPS-Endpunkte: ob sie antworten, mit welchem Statuscode, wie schnell und wie lange das Zertifikat
noch gilt. Das Modul ist ab Werk aus; bis zu 16 URLs werden gleichzeitig gefragt, der Inhalt der Antwort wird nicht
gelesen.

## Voraussetzungen

Der Agent erreicht nur Ziele, die der Host erlaubt: `module_targets` im Abschnitt `[local]` von
`/etc/sermon/agent.toml`, ab Werk nur Loopback (`127.0.0.0/8`, `::1`). Wer einen Dienst im Netz prüfen will, ergänzt
dort dessen Netz. Ein Name wird einmal aufgelöst, jede Adresse muss erlaubt sein.

## Einstellungen

- **URLs:** `http://` oder `https://`, ohne Benutzer und Passwort. Die Query (`?…`) wird gesendet, erscheint aber nicht
  im Label der Metriken.
- **Erwartete Statuscodes:** leer heißt 200 bis 399.
- **Timeout:** je Anfrage mit allen Weiterleitungen, höchstens 25 s.
- **Weiterleitungen folgen:** höchstens drei, nur zu erlaubten Zielen, nie von HTTPS zu HTTP.
- **TLS-Zertifikate prüfen:** ab Werk an. Für ein selbst signiertes Zertifikat besser eine **CA-Datei** angeben (unter
  `module_file_prefixes`, ab Werk `/etc/ssl`, `/etc/pki`, `/etc/letsencrypt`) als die Prüfung abzuschalten.

## Metriken

- `sermon_http_check_up`: 1 bei einer Antwort mit erwartetem Status.
- `sermon_http_check_status_code`, `sermon_http_check_duration_seconds` (bis zu den Headern der letzten Antwort).
- `sermon_http_check_cert_expires_in_seconds`: Restlaufzeit des Zertifikats des konfigurierten Hosts.

## Wenn es nicht läuft

- „… is not in module_targets of agent.toml“ (Zustand `vetoed`): Das Ziel oder eine Weiterleitung liegt außerhalb
  der erlaubten Netze.
- `up` 0 ohne Statuscode: keine Antwort (Verbindung abgelehnt, Timeout, TLS-Fehler).
