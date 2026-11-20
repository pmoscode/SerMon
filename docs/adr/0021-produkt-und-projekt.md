# 0021 – Produkt und Projekt: Sprachen, Lizenz, Reihenfolge, Monorepo

- Status: accepted
- Datum: 2026-11-20 (Entscheidung aus der Konzeptrunde, nachträglich dokumentiert)
- Bezug: KONZEPT.md §2 (D20, D21, D24, D25), §17, §19, §20, §21, §23; ADR 0003, 0006

## Kontext

Die Zielgruppe arbeitet auf Deutsch und Englisch; viele Nutzer sind keine Monitoring-Experten. SerMon soll Open Source
sein, ohne dass ein Anbieter es als geschlossenen Dienst weiterverkauft. Das Projekt muss früh nützlich sein und
klein anfangen. Agent, Server, UI und Protokoll ändern sich oft gemeinsam.

## Optionen

1. **Sprache:** nur Englisch, oder DE + EN von Anfang an mit typsicherem i18n.
2. **Lizenz:** MIT/Apache (frei weiterverwendbar, auch geschlossen), **AGPL-3.0** (Änderungen auch bei SaaS offen),
   proprietär.
3. **Reihenfolge:** alles parallel, oder Ausbaustufen mit jeweils nutzbarem Ergebnis.
4. **Repository:** eines pro Komponente oder ein Monorepo.

## Entscheidung

- **D20 – UI auf Deutsch und Englisch**, Paraglide (typsicher, Tree-Shaking), Kontexthilfe zu jeder Kennzahl und Seite
  (`docs/help/{de,en}`).
- **D21 – AGPL-3.0** für alle Teile.
- **D24 – Reihenfolge Metriken → Alerts → Logs → Updates** (M1 bis M4), danach Module.
- **D25 – GitHub-Monorepo mit GitHub Actions;** `just` als einheitlicher Einstieg, gemeinsame Version für Server und
  Agent (SemVer, Server unterstützt Agent N und N-1). CI und Supply Chain regelt ADR 0006.

## Konsequenzen

- Positiv: Jede Ausbaustufe ist für sich nutzbar; Protokolländerungen landen in einem Commit über alle Komponenten;
  Hilfe und Übersetzungen werden von CI auf Vollständigkeit geprüft.
- Negativ: Jeder Text existiert zweimal; die AGPL schreckt manche Unternehmen ab; das Monorepo braucht drei
  Toolchains in CI.
- Security: Ein Repository mit einheitlichen Prüfungen (Secret-Scan, Abhängigkeiten, CodeQL) für alle Teile.
- Folgearbeiten: Markenprüfung des Namens vor dem ersten Release (O7).
