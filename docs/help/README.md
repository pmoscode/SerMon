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
