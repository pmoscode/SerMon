---
name: sermon-adr
description: Write an Architecture Decision Record for SerMon in docs/adr/ (MADR format). Use when a technical decision is made or changed — choosing a library/database/protocol, adding a new helper operation, changing security boundaries, or deviating from KONZEPT.md.
---

# Write an ADR

## When
- New or replaced dependency with architectural impact, storage/protocol changes, new root-helper operation, new rule type, security boundary changes, anything that contradicts KONZEPT.md §2 (decision log).

## Steps
1. Next number: `ls docs/adr | sort | tail -1` → `NNNN+1`, file `docs/adr/NNNN-kebab-title.md`.
2. Use this template (German or English, consistent with existing ADRs):

```markdown
# NNNN – <Titel>

- Status: proposed | accepted | superseded by NNNN
- Datum: YYYY-MM-DD
- Bezug: KONZEPT.md §x, Dx

## Kontext
Problem, Randbedingungen, Kräfte (Security, Einfachheit, Performance, Betrieb).

## Optionen
1. Option A – Vor-/Nachteile
2. Option B – Vor-/Nachteile
3. …

## Entscheidung
Gewählt: Option X, weil …

## Konsequenzen
- Positiv: …
- Negativ / Risiken: … (und Gegenmaßnahmen)
- Security-Auswirkungen: …
- Folgearbeiten: …
```

3. If the decision changes KONZEPT.md, update the decision log (§2) and the affected section, referencing the ADR number.
4. A superseded ADR is never deleted – set status `superseded by NNNN`.
