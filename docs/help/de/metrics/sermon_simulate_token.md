# Simuliertes Geheimnis angekommen

1, wenn das Testmodul `simulate` sein Geheimnis `token` bekommen hat, sonst 0. Den Wert selbst meldet es nie.

## Einordnung

Damit lässt sich prüfen, dass Zugangsdaten eines Moduls den Agent erreichen. Das Modul gibt es nur in Builds mit dem
Feature `simulate` (`just simulate`), nie in einem Release.

Metrik: `sermon_simulate_token`
