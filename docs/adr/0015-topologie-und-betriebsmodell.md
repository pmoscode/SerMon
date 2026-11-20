# 0015 – Topologie und Betriebsmodell: Push mit Puffer, self-hosted, mandantenfähig, docker-compose

- Status: accepted
- Datum: 2026-11-20 (Entscheidung aus der Konzeptrunde, nachträglich dokumentiert)
- Bezug: KONZEPT.md §2 (D4, D5, D6, D23), §3, §5, §22; ADR 0001, 0007

## Kontext

Die Nodes stehen oft hinter NAT oder Firewalls, in verschiedenen Netzen und bei verschiedenen Kunden. Ein Monitoring,
das Nodes von außen abfragt, bräuchte eingehende Ports auf jedem Node. Betreiber wollen die Daten selbst halten,
später soll ein SaaS-Angebot ohne Umbau möglich sein. Die Zielgröße sind kleine bis mittlere Flotten.

## Optionen

1. **Pull** (Server fragt Nodes ab, wie Prometheus): einfache Server-Logik, aber eingehende Ports auf jedem Node und
   Datenverlust bei Netzausfällen.
2. **Push ohne Puffer:** keine eingehenden Ports, aber Lücken bei Ausfällen von Netz oder Server.
3. **Push mit lokalem Puffer:** keine eingehenden Ports, nichts geht verloren, solange der Puffer reicht.
4. Betrieb **nur als SaaS**, **self-hosted für eine Organisation** oder **self-hosted und mandantenfähig**.
5. Deployment als **Kubernetes-Chart**, **docker-compose** oder einzelne Pakete.

## Entscheidung

- **D4 – Zentraler Server, Agents pushen** über eine ausgehende mTLS-Verbindung und puffern lokal in einem WAL
  (24 h bzw. 200 MB, was zuerst erreicht ist). Der Server bestätigt Batches erst nach dem Schreiben (at-least-once,
  Deduplizierung über die Batch-Seq).
- **D5 – Self-hosted und mandantenfähig von Anfang an:** Jede fachliche Tabelle hat eine `org_id`, die Trennung ist in
  der Anwendung und per Row-Level-Security abgesichert (ADR 0007).
- **D6 – Zielgröße < 50 Nodes** pro Instanz. Messungen und Grenzen in ADR 0001; der Metrik-Speicher bleibt
  austauschbar.
- **D23 – Deployment per docker-compose:** Caddy (TLS), sermon-server, PostgreSQL mit TimescaleDB und Loki, getrennte
  Netze, gehärtete Container.

## Konsequenzen

- Positiv: Auf den Nodes ist kein eingehender Port nötig; Ausfälle von Netz oder Server kosten keine Daten; das
  Datenmodell muss für SaaS nicht umgebaut werden; Installation und Upgrade mit `docker compose`.
- Negativ: Der Server muss viele lange Streams halten und Backpressure beherrschen; Mandantentrennung muss überall
  mitgedacht werden (Isolationstests für jede RPC).
- Security: Der Agent ist immer Client; Identität über Client-Zertifikate (ADR 0009, 0010). Mandantendaten verlassen
  die Instanz nicht.
- Folgearbeiten: Kein HA in v1 (O11); Worker mit Leases in PostgreSQL halten mehrere Instanzen offen.
