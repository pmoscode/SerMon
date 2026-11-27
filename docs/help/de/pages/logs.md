# Logs

Die Log-Zeilen der Nodes, die du sehen darfst – aus dem systemd-Journal und aus Logdateien – mit Filtern, Histogramm, Kontext und Live-Ansicht. Die Logs bleiben bis zu 14 Tage gespeichert, je nach Einstellung der Organisation ([Aufbewahrung](help:concepts/retention)).

## Filtern

- **Zeitraum:** 15 Minuten bis 14 Tage bis jetzt. Wer im Histogramm einen Bereich mit der Maus markiert, sieht nur diesen Ausschnitt; **Zurück zu …** kehrt zum gewählten Zeitraum zurück.
- **Node** und **Gruppe**, **Quelle** (Journal oder Datei), **Units** (systemd-Units oder Syslog-Kennungen, mit Komma getrennt) und **Schweregrad** („ab Warnung“ zeigt Warnungen und alles Schwerere).
- **Text** sucht einen Teil der Zeile genau wie eingegeben, auf Wunsch ohne Rücksicht auf Groß- und Kleinschreibung. Reguläre Ausdrücke und Abfragesprachen gibt es bewusst nicht.
- Alle Filter stehen in der Adresse der Seite: Ein Link zeigt anderen genau diese Ansicht, soweit sie die Nodes sehen dürfen.

## Das Histogramm

- Die Balken zählen die Zeilen des Filters je Schritt, etwa 100 über den Zeitraum, ab 100 Minuten Zeitraum in ganzen Minuten, ab 2 Tagen in ganzen Stunden. Der erste und der letzte Balken können ein wenig über den Zeitraum hinausreichen.
- Ohne Text und Units kommt das Histogramm aus Zählungen, die der Server beim Speichern der Zeilen anlegt – auch über 14 Tage in einem Augenblick. Mit Text oder Units muss die Log-Ablage jede Zeile des Zeitraums lesen; über lange Zeiträume dauert das zu lange, dann bittet das Histogramm, den Zeitraum zu verkürzen. Die Zeilen selbst laden trotzdem.

## Die Liste lesen

- Die neuesten Zeilen stehen oben. **Ältere Zeilen laden** holt die nächsten 200; die Liste zeigt höchstens 2 000 Zeilen.
- Zeilen erscheinen immer als Text. Farb- und Steuercodes von Terminals werden entfernt; unsichtbare Zeichen und Zeichen, die die Leserichtung umdrehen, erscheinen markiert als Symbol oder als `⟨U+202E⟩`. So kann eine Zeile nichts vortäuschen.
- **gekürzt** heißt: Der Agent hat eine sehr lange Zeile abgeschnitten (16 KiB).
- Ein Klick auf die Uhrzeit zeigt die weiteren Felder (Prozess, PID, Datei …) und **Zeilen davor und danach**: bis zu 50 Zeilen vor und nach dem Eintrag aus derselben Quelle und Unit.
- Passwörter, Tokens und ähnliche Geheimnisse ersetzt schon der Agent durch `***`, bevor eine Zeile den Host verlässt.

## Live

**Live** zeigt neue Zeilen des Filters, sobald sie ankommen (nach wenigen Sekunden), oben in der Liste. **Pause** hält die Liste still; neue Zeilen warten und erscheinen mit **Fortsetzen**. Kommen mehr als 100 Zeilen pro Sekunde, zeigt Live nur die neuesten und sagt das. Zeilen, die ein Agent nach einem Ausfall verspätet nachliefert, stehen in der normalen Abfrage, nicht unbedingt in Live.

## Warum Zeilen fehlen können

- Der Node verbietet in seiner `agent.toml` das Senden von Logs (`allow_logs = false`); die Seite nennt solche Nodes.
- Der Agent hat Journal-Einträge verloren, weil das Journal aufgeräumt wurde, während er stand; die Seite nennt Node und ungefähre Anzahl.
- Die Einstellungen der Gruppe senden nur einen Teil (Schweregrad, ausgeschlossene Units) oder gar keine Logs.
- Viewer sehen Logs nur, wenn die Organisation es erlaubt.

## Typische nächste Schritte

1. Von einem Chart oder Alert über **Logs** direkt zum Zeitpunkt springen.
2. Im Histogramm die Spitze markieren und nach **ab Fehler** filtern.
3. Den Kontext eines Eintrags öffnen, um zu sehen, was davor geschah.
