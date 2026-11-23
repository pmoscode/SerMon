# Gesendete Log-Einträge

Log-Einträge, die der Agent seit seinem Start in seinen Puffer zum Senden geschrieben hat: aus dem Journal bzw. der
Logdatei, nach den Log-Einstellungen der Gruppe und nach der Redaction.

## Einordnung

- Der Wert steigt mit dem Logaufkommen des Hosts.
- Bleibt er bei 0, sind Logs für die Gruppe abgeschaltet, der Agent verbietet sie lokal (`allow_logs = false` in
  `/etc/sermon/agent.toml`), oder er hat noch keine Konfiguration vom Server.

Metrik: `sermon_agent_logs_sent_entries`
