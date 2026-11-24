# Live-Ansicht der Prozesse

Die Prozesse des Hosts wie in `top`, alle 2 Sekunden neu. Der Agent schickt sie nur, solange jemand zusieht; gespeichert wird davon nichts.

## Kopfzeile

- **CPU:** der belegte Anteil aller CPUs seit dem letzten Stand. Im ersten Stand fehlt er noch (–).
- **Speicher:** belegt ist, was nicht verfügbar ist (Gesamt minus „available“); Cache zählt also nicht als belegt.
- **Swap**, **Load** über 1, 5 und 15 Minuten, **Uptime** und die Prozesse je Zustand.

## Liste

- **CPU** ist der Anteil eines Kerns: Ein Prozess mit mehreren Threads kann über 100 % kommen, wie in `top`.
- **MEM** und **RSS:** der Arbeitsspeicher, den der Prozess gerade belegt.
- Ein Klick auf eine Spalte sortiert, ein zweiter dreht die Reihenfolge um. Nicht lesbare Werte („n/a“) stehen immer am Ende.
- Die **Filter** wirken im Browser: User, ein Teil des Namens (ohne Groß- und Kleinschreibung) und ein regulärer Ausdruck über Name und Befehlszeile. Ein ungültiger Ausdruck filtert nicht und wird angezeigt.
- Die **Baumansicht** ordnet die Prozesse unter ihren Elternprozess. Mit Filter erscheinen die Eltern eines Treffers abgeblendet dazu.
- **Anhalten** hält die Anzeige fest, die Daten laufen im Hintergrund weiter; **Weiter** zeigt den neuesten Stand.
- In Klammern stehen Kernel-Threads, etwa `[kworker/0:1]`.

## Grenzen

- Der Agent schickt höchstens 4 000 Prozesse, die aktivsten zuerst; die übrigen werden nur gezählt.
- Befehlszeilen sind wie Logs redigiert (Secrets ersetzt) und auf 1 KiB gekürzt. Admins können sie für die Organisation ganz ausblenden (Administration → Logs). Wer die Logs des Nodes nicht lesen darf – Betrachter einer Organisation, die ihnen Logs verwehrt –, sieht auch keine Befehlszeilen.
- Die Ansicht liest nur. SerMon beendet oder startet keine Prozesse.
- Steht in der `agent.toml` des Hosts `allow_live_view = false`, lehnt der Agent ab und nennt den Grund.
- Ist der Agent gerade nicht mit dem Server verbunden, steht das über der Liste; die Ansicht läuft weiter, sobald er sich verbindet.
