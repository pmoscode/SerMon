# TLS-Zertifikate

Wie lange Zertifikate noch gelten, ob ihre Kette zu einer vertrauenswürdigen CA führt und ob der Name passt – für
Zertifikatsdateien des Hosts und für TLS-Endpunkte. Das Modul ist ab Werk aus und prüft alle 5 Minuten; den Host
verlassen nur diese Angaben, nie ein Zertifikat oder Schlüssel.

## Voraussetzungen

- **Dateien** nur unter `module_file_prefixes` in `[local]` von `/etc/sermon/agent.toml` (ab Werk `/etc/ssl`,
  `/etc/pki`, `/etc/letsencrypt`); Symlinks werden aufgelöst und erneut geprüft. Der Agent läuft als `sermon` und
  muss die Datei lesen dürfen. Zertifikate unter `/etc/letsencrypt/live/` sind meist lesbar, die Schlüssel daneben
  nicht – sie braucht das Modul auch nicht. Enthält eine Datei auch den Schlüssel (haproxy), liest das Modul nur die
  Zertifikate.
- **Endpunkte** nur unter `module_targets` (ab Werk Loopback).

## Einstellungen

- **Zertifikatsdateien:** PEM, das Zertifikat des Dienstes zuerst (etwa `fullchain.pem`).
- **Endpunkte:** `host:port`, etwa `localhost:443`.
- **Servername:** der Name, der an die Endpunkte gesendet und im Zertifikat geprüft wird; leer heißt der Host des
  Endpunkts. Für `127.0.0.1:443` mit einem Zertifikat für `example.org` hier `example.org` eintragen.
- **CA-Datei:** prüft die Ketten gegen diese CAs statt gegen die des Systems (eigene CA).

## Metriken

- `sermon_tls_cert_expires_in_seconds`, `sermon_tls_chain_expires_in_seconds`: Restlaufzeit des Zertifikats und der
  ganzen Kette.
- `sermon_tls_chain_valid`: Die Kette führt zu einer vertrauenswürdigen CA (ein abgelaufenes Zertifikat zählt hier
  nicht).
- `sermon_tls_name_valid`: nur Endpunkte, das Zertifikat gilt für den Namen.
- `sermon_tls_up`: Zertifikate wurden gelesen.

## Wenn es nicht läuft

- Zustand `vetoed`: Datei oder Endpunkt liegen außerhalb der erlaubten Pfade bzw. Netze.
- `sermon_tls_up` 0 für eine Datei: Sie fehlt, ist nicht lesbar (Rechte für `sermon`) oder kein PEM.
