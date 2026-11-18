---
name: sermon-ui-component
description: Build or change SerMon web UI pages and components (SvelteKit SPA, Svelte 5 runes, Tailwind, shadcn-svelte, uPlot charts) with i18n DE/EN, contextual help, accessibility and secure rendering. Use for any work in web/.
---

# SerMon UI work

Reference: KONZEPT.md §4.4, §16 (dashboards), §17 (UX & help), §18.3 (web security).

## Conventions
- **Svelte 5 runes only** (`$state`, `$derived`, `$effect`, `$props`); no legacy `export let` / stores for new code unless shared app state (`web/src/lib/stores/*.svelte.ts`).
- Components: `web/src/lib/components/<area>/<Name>.svelte`, one responsibility each; base UI from shadcn-svelte (`$lib/components/ui`) – don't hand-roll buttons/dialogs.
- Data: only via generated Connect clients (`$lib/api`); loading/error/empty states for every async view. Live data via server-streaming with cleanup in `$effect` return.
- Charts: use the shared uPlot wrapper `$lib/charts/TimeSeries.svelte` (synced crosshair/zoom via the global time range store), unit formatting via `$lib/format` (bytes IEC, bits/s, percent, durations; locale aware).
- Styling: Tailwind tokens; light/dark via CSS variables; status never by color alone (icon + text).

## i18n & help (mandatory)
- No hard-coded user-visible strings. Paraglide messages in `web/messages/de.json` and `web/messages/en.json` – add **both**.
- Every metric/KPI shows an ⓘ `<HelpTip key="metrics.cpu.iowait" />` whose content comes from `docs/help/{de,en}/…md`. New page → add a context help entry (`docs/help/{de,en}/pages/<page>.md`) for the `?` panel.
- Empty states explain what to do next (e.g. link to "Add node").

## Security
- **Never** use `{@html}` for data from the API (log lines, hostnames, process names, usernames, alert text). Markdown help is rendered at build time from repo files only.
- No inline scripts/styles that would require weakening CSP.
- **Everything is served locally (decision D27):** never load anything from a CDN or third-party host – no Google Fonts,
  no `<script src="https://…">`, no remote images/icons, no analytics. Install libraries, fonts (`@fontsource/*`) and
  icon sets via pnpm so Vite bundles them. The E2E network guard (`web/tests/e2e/`) fails on any request to a foreign host.
- Don't store tokens in localStorage; session is an HttpOnly cookie.
- Hide UI actions the role can't perform, but remember the server is the authority.

## Accessibility
- Keyboard reachable, visible focus, proper labels/ARIA for icon buttons, `aria-live` for live-updating KPIs (polite, throttled), charts have a textual summary/table alternative.

## Tests (TDD – `sermon-tdd`)
- Vitest + Testing Library for logic and component behaviour (`*.test.ts` next to the component).
- Playwright E2E for user flows in `web/tests/e2e/`, including `@axe-core/playwright` check on each new page.
- Run: `pnpm -C web check && pnpm -C web lint && pnpm -C web test`.

## Checklist
- [ ] Runes, shadcn base components, shared chart/format helpers
- [ ] Strings DE + EN, ⓘ help + page help
- [ ] Loading/error/empty states
- [ ] No `{@html}` on API data, CSP intact
- [ ] Unit + E2E + axe green
