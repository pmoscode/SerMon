# Simulierter Wert

Der Wert, den das Testmodul `simulate` laut seiner Konfiguration meldet (Feld „Value“).

## Einordnung

Das Modul gibt es nur in Builds mit dem Feature `simulate` (`just simulate`), nie in einem Release. Es erreicht
nichts auf dem Host und dient dazu, Module, Alerts und die UI ohne echten Dienst auszuprobieren.

Metrik: `sermon_simulate_value`
