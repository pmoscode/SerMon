# Unlesbare Journal-Zeilen

Zeilen von `journalctl`, die der Agent seit seinem Start nicht als Journal-Eintrag lesen konnte: kein gültiges JSON,
länger als 1 MiB oder ohne Position im Journal. Der Agent überspringt sie; alle anderen Einträge kommen an.

## Einordnung

Einzelne Zeilen sind unkritisch. Steigt der Wert ständig, liefert `journalctl` auf diesem System eine Ausgabe, die der
Agent nicht versteht – dann bitte die systemd-Version melden (`journalctl --version`).

Metrik: `sermon_agent_journal_invalid_lines`
