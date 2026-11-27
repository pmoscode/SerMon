# Hilfetexte

Quelle für ⓘ-Tooltips und das Hilfe-Panel der UI (KONZEPT §17). Jedes Thema gibt es auf Deutsch und Englisch:
`de/<thema>.md` und `en/<thema>.md`, z. B. `de/pages/fleet.md`. Die Web-UI baut daraus beim Build pro Sprache ein
JSON (`web/scripts/help.js`); `pnpm run lint:help` (Teil von `pnpm run lint`) prüft Vollständigkeit und Format.

## Aufbau einer Datei

```markdown
# Titel

Zusammenfassung in einem Absatz – erscheint im ⓘ-Tooltip.

## Abschnitt

Absätze, Listen (`-` oder `1.`), Code-Blöcke, **fett**, *kursiv*, `Code` und Links:
[anderes Thema](help:figures/cpu_busy), [Seite der App](/fleet) oder https-Links.
```

Nicht unterstützt: Tabellen, HTML, Überschriften ab `####`. Alles wird als Text dargestellt, nie als HTML.

## Themen

| Ordner      | Inhalt                                                        |
|-------------|---------------------------------------------------------------|
| `pages/`    | Kontexthilfe einer Seite (`?`-Taste), Zuordnung in `web/src/lib/help/pages.ts` |
| `figures/`  | Kennzahlen der Übersichten                                    |
| `metrics/`  | Metriken des Katalogs, Dateiname = Metrikname                 |
| `concepts/` | Übergreifendes, z. B. Charts und Zeitraum                     |
| `modules/`  | Module des Agents, Dateiname = Modul-ID                       |
| `channels/` | Kanaltypen der Benachrichtigungen, Dateiname = Typ            |

Regeln haben ihren Hilfetext in ihrer YAML-Datei (`spec.help` in `rules/`), nicht hier.

## Prüfungen (V1-10)

- `pnpm run lint:help` (`web/scripts/help.js`): jedes Thema in beiden Sprachen und lesbar; jedes Thema, das die UI
  als Text nennt, existiert; jedes Thema wird **gezeigt** – von der UI genannt, aus einem Namen des Servers gebildet
  (`metrics/`, `modules/`, `channels/`) oder von einem anderen Thema verlinkt; die UI-Texte (`web/messages/`) haben
  in beiden Sprachen dieselben Schlüssel und Platzhalter; das [Benutzerhandbuch](../user/README.md) verlinkt nur
  vorhandene Texte, in beiden Sprachen dieselben, und jede Seite (`pages/`).
- `server/cmd/sermon-server/help_test.go`: ein Text in beiden Sprachen für jede Metrik des Katalogs (auch die der
  Module), jedes Modul, jeden Kanaltyp, und Name und Hilfe jeder Standardregel in beiden Sprachen.
