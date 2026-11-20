# 0020 – Updates, Logs und Module

- Status: accepted
- Datum: 2026-11-20 (Entscheidung aus der Konzeptrunde, nachträglich dokumentiert)
- Bezug: KONZEPT.md §2 (D15, D17, D18), §10, §14, §15; ADR 0016

## Kontext

Neben Metriken soll SerMon Paket-Updates steuern, Logs sammeln und gängige Dienste überwachen. Jeder dieser Bereiche
greift tiefer ins System ein als Metriken: Updates brauchen root, Logs enthalten Geheimnisse, Module brauchen
Zugangsdaten zu Datenbanken. Fremder Plugin-Code auf allen Nodes wäre ein Supply-Chain-Risiko.

## Optionen

1. **Updates:** nur anzeigen, automatisch ohne Steuerung, oder über Policies pro Gruppe mit Wartungsfenster.
2. **Logs:** nur journald, nur Dateien, oder alle üblichen Quellen.
3. **Module:** Plugins von Dritten (flexibel, aber fremder Code), eingebaute Module, oder nur ein generischer Scraper.

## Entscheidung

- **D15 – Updates policy-basiert pro Node-Gruppe:** `report`, `security-auto` oder `all-auto`, mit Wartungsfenster,
  Rolling Rollout (höchstens N parallel, Stopp bei Fehlern) und lokalem Veto im Helper. Ausgeführt nur über die festen
  Helper-Operationen (ADR 0016).
- **D17 – Logs aus journald, klassischen Dateien, Docker und beliebigen Dateien per Glob;** Redaction im Agent vor
  dem Versand, Speicherung in Loki pro Org. Die journald-Anbindung regelt ADR 0013.
- **D18 – Module im Agent eingebaut, standardmäßig aus**, zentral pro Gruppe konfiguriert (JSON-Schema), dazu ein
  Prometheus-Scraper für alles Übrige. Kein Fremdcode.

## Konsequenzen

- Positiv: Updates bleiben nachvollziehbar und begrenzt; Geheimnisse verlassen den Node nicht unredigiert; Module sind
  getestet und mit dem Agent signiert.
- Negativ: Neue Module erfordern ein Agent-Release; Alpine hat keine Security-Metadaten (nur `report`/`all-auto`, O6);
  der Docker-Socket ist root-äquivalent (Modul aus, Warnung, cgroup-Variante bevorzugt, O4).
- Security: Modul-Zugangsdaten Ende-zu-Ende verschlüsselt und nur im Speicher des Agents; Scraper nur auf localhost oder
  Allowlist; Serien-Limit pro Node.
- Folgearbeiten: Logs in M3, Updates in M4, Module in M5 (Roadmap §23).
