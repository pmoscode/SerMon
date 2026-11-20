# 0018 – Retention: gestaffelte Auflösung für Metriken, Logs und Events

- Status: accepted
- Datum: 2026-11-20 (Entscheidung aus der Konzeptrunde, nachträglich dokumentiert; DB-Auflösung nach Spike M0-09)
- Bezug: KONZEPT.md §2 (D12, D13), §4.5, §8; ADR 0001

## Kontext

Der Agent sammelt jede Sekunde rund 400 Serien. Für die Fehlersuche sind die letzten Minuten in voller Auflösung
wichtig, für Trends und Prognosen Wochen bis ein Jahr in grober Auflösung. Alles in voller Auflösung zu speichern,
sprengt Speicher und Schreiblast (Spike M0-09: rund 320 GB WAL pro Tag bei 1 s für 50 Nodes).

## Optionen

1. **Eine Auflösung, lange Retention:** einfach, aber entweder zu grob für Live-Analysen oder zu teuer.
2. **Gestaffelt mit Downsampling** in der Datenbank (Continuous Aggregates).
3. **Rohdaten extern** (z. B. Objektspeicher), Abfragen bei Bedarf: mehr Komponenten.

## Entscheidung

**D12 – Gestaffelte Retention, konfigurierbar:**

| Daten | Auflösung | Ort | Standard |
|---|---|---|---|
| Metriken | 1 s | Ringpuffer im Server-Speicher | 15 min |
| Metriken | 10 s (avg/min/max) | `metrics_raw` | 24 h |
| Metriken | 1 min | `metrics_1m` | 30 Tage |
| Metriken | 1 h | `metrics_1h` | 1 Jahr |
| Logs | – | Loki | 14 Tage |
| Events, Alerts, Update-Läufe | – | PostgreSQL | 1 Jahr |
| Audit-Log | – | PostgreSQL | 2 Jahre |

Speicher und Schema regelt ADR 0001 (D13).

## Konsequenzen

- Positiv: Live-Charts und kurze Alert-Fenster nutzen 1 s, ohne die Datenbank zu belasten; ein Jahr Trend für < 5 GB.
- Negativ: Nach einem Server-Neustart fehlen die 1-s-Werte der letzten 15 min (nicht persistent); Spitzen unter 10 s
  sind in der Datenbank nur über `max` sichtbar.
- Security/Datenschutz: Logs können personenbezogene Daten enthalten; 14 Tage sind Standard und kürzer einstellbar
  (§18.7).
- Folgearbeiten: Retention-Jobs für Events und Audit-Log (M2/M3); Einstellungen pro Org.
