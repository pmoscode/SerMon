# 0011 – Eigene Metriken des Servers: `/metrics` auf eigenem Listener

- Status: accepted
- Datum: 2026-11-20
- Bezug: KONZEPT.md §4.3 (Observability), §18; IMPLEMENTIERUNGSPLAN M1-24, M1-26

## Kontext

KONZEPT §4.3 sieht einen eigenen `/metrics`-Endpunkt im Prometheus-Format vor. Die Zähler des Ingests (M1-24) gibt es
schon, sie waren bisher nur im Speicher. Zu entscheiden war, wo der Endpunkt erreichbar ist, wer ihn lesen darf, was
er preisgibt und womit er gebaut wird.

## Entscheidung

- **Eigener Listener `SERMON_METRICS_ADDR`, standardmäßig aus.** Empfohlen ist `127.0.0.1:9464`. Er liefert nur
  `GET /metrics`, ohne Anmeldung. Der Web-Port bleibt unverändert: Dort gibt es keinen Pfad ohne Session außer
  Health und UI. Liegt die Adresse nicht auf Loopback, warnt der Server beim Start.
- **Keine Mandantendaten.** Nur Werte der Instanz: Ingest-Zähler, Lag, Agent-Sessions, Live-Streams, Go-Laufzeit,
  Version. Keine Org-, Gruppen-, Node- oder Hostnamen als Labels; damit ist der Endpunkt auch für den Betreiber ohne
  Mandantenrechte unkritisch.
- **Eigenes kleines Paket `platform/promtext`** für das Textformat 0.0.4, statt `prometheus/client_golang`. Für eine
  Handvoll Zähler und Gauges reichen rund 100 Zeilen. Wir vermeiden damit einen größeren Abhängigkeitsbaum (Supply
  Chain, ADR 0006) und globale Register. Die Werte werden beim Abruf über Funktionen gelesen.
- Namen beginnen mit `sermon_server_`, damit sie nicht mit den Agent-Metriken (`sermon_…`) kollidieren.

## Alternativen

- **Auf dem Web-Port mit Bearer-Token:** ein weiteres Geheimnis, das verwaltet und rotiert werden muss, und ein Pfad
  am öffentlichen Port, der ohne Session funktioniert. Verworfen; ein Reverse Proxy kann den Listener bei Bedarf mit
  eigener Anmeldung veröffentlichen.
- **`client_golang`:** Standard, aber für diesen Umfang zu groß. Wird es nötig (Histogramme, viele Metriken), ist der
  Wechsel lokal begrenzt.

## Folgen

- Wer die Adresse öffentlich bindet, macht Betriebswerte ohne Anmeldung lesbar; die Warnung beim Start und die Doku
  weisen darauf hin.
- Neue Metriken werden in `cmd/sermon-server` registriert; ein Test prüft Format und Escaping.
