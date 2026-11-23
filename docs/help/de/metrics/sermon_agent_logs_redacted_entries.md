# Redigierte Log-Einträge

Log-Einträge, in denen der Agent seit seinem Start vor dem Senden etwas durch `***` ersetzt hat: Passwörter, Tokens,
Schlüssel und Treffer der Redaction-Regeln der Organisation.

## Einordnung

Ein steigender Wert ist kein Fehler, sondern zeigt, dass Geheimnisse in Logs stehen. Dann lohnt es sich, den Dienst so
einzustellen, dass er sie gar nicht erst schreibt.

Metrik: `sermon_agent_logs_redacted_entries`
