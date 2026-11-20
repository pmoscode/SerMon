# Verlorene Journal-Einträge

Einträge des systemd-Journals, die der Agent seit seinem Start nicht lesen konnte, weil sie schon gelöscht waren. Der
Agent erkennt das an Lücken in den Sequenznummern des Journals.

## Einordnung

- 0 ist der Normalfall.
- Werte über 0 heißen: Für diese Zeit fehlen Security-Events und Logs. Typisch ist das nach einem längeren Stopp des
  Agents, wenn das Journal inzwischen aufgeräumt wurde (`journalctl --vacuum-*`, `SystemMaxUse`).

## Nächste Schritte

1. Prüfen, ob der Agent länger gestoppt war.
2. Die Größe des Journals erhöhen (`SystemMaxUse` in `journald.conf`), wenn es sehr schnell rotiert.

Metrik: `sermon_agent_journal_lost_entries`
