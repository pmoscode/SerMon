# Arbeitsspeicher

Der belegte Anteil des Arbeitsspeichers wie bei `free`: Gesamtspeicher minus dem, was Programme ohne Auslagern noch bekommen können. Caches und Puffer zählen nicht, denn Linux gibt sie bei Bedarf frei.

## Einordnung

- Werte bis etwa 85 % sind unkritisch.
- Steigt der Wert weiter und wird **Swap** genutzt, wird der Server langsamer. Ist auch der Swap voll, beendet der Kernel Prozesse (OOM-Killer).
- Ein langsam, aber stetig steigender Wert kann auf ein Speicherleck in einem Dienst hindeuten.

## Nächste Schritte

1. Im Node-Detail den Verlauf und die Swap-Nutzung prüfen.
2. Die Prozesse mit dem größten Speicherbedarf suchen, z. B. mit `ps aux --sort=-rss | head`.
