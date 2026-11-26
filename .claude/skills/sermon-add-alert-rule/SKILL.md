---
name: sermon-add-alert-rule
description: Add or change a default alert rule in SerMon (rules/*.yaml) including validation, engine tests, help texts and notification fields. Use when asked for a new alert (CPU, memory, disk, network, security, updates, reboot, anomaly) or to tune default thresholds.
---

# Add a default alert rule

Reference: KONZEPT.md §12 (rule types, state machine, standard rules table).

## 1. Choose the rule type
| Type | Use for |
|---|---|
| `threshold` | aggregated metric over window vs. warning/critical (with `clear` hysteresis and `for`) |
| `rate` | per-second/minute rate of a counter (errors, drops, overflows) |
| `event` | security/system events (`ssh.login.failed`, `system.reboot`, …), optional `groupBy` + `count` window |
| `absence` | missing data/heartbeat (node offline) |
| `state` | discrete state (unit failed, interface down, reboot required, fs read-only) |
| `prediction` | linear forecast (disk full in < X h) |
| `anomaly` | node anomaly rate |

Do **not** introduce raw query expressions. If no type fits, extend the engine with a new typed rule kind (TDD, ADR via `sermon-adr`).

## 2. Write the rule
File: `rules/<category>/<id>.yaml`. The directory is the `category` label, the file name the `id`
(`<area>.<subject>.<condition>`, e.g. `system.filesystem.inodes_high`); the loader rejects anything else.
The schema is `proto/sermon/web/v1/alert_rule.proto` (KONZEPT §12.2); after editing run `just gen-rules`, which copies
`rules/` into the server (`server/internal/app/alerting/ruleset/defaults`, embedded; `just check-gen` fails on a stale copy).

```yaml
apiVersion: sermon/v1
kind: AlertRule
metadata:
  id: system.filesystem.inodes_high
  name: { de: "Inodes fast erschöpft", en: "Inodes almost exhausted" }
  labels: { category: disk }
spec:
  type: threshold
  metric: sermon_filesystem_inodes_used
  relativeTo: sermon_filesystem_inodes_total   # same labels; the ratio is compared as 0..1
  dimensions: [ mountpoint ]
  aggregate: last
  window: 5m
  warning:  { op: ">", value: 0.80 }
  critical: { op: ">", value: 0.90 }
  clear:    { op: "<", value: 0.75 }
  for: 5m
  help:
    de: "…Was bedeutet das, typische Ursachen, was tun…"
    en: "…"
```

Rules for good defaults:
- Base thresholds on the Netdata health defaults / KONZEPT §12.3 table; avoid noisy alerts – always use `for` and `clear`.
- Filter (`labels`) and `dimensions` together must name every label of the metric (see the catalog,
  `server/internal/domain/metric/catalog.go`). Exclude irrelevant dimension values with `exclude` (glob lists); the agent
  already leaves out tmpfs, squashfs, overlay, loop devices and container interfaces.
- Ratios are 0..1 (`0.85`, not `85`) – the loader rejects anything else. Fields of another rule type are errors.
- A rule on a module's metric (`rules/modules/module.<id>.<condition>.yaml`, category `modules`) names the module:
  `selector: { module: <id> }` – it then applies only to nodes whose group runs the module (M5-20); the loader
  refuses a module metric without it.
- `help` is mandatory in DE and EN: meaning, typical causes, concrete next steps (link to Live view / Logs where useful).
- For security events specify `notifyFields` (e.g. `[ts, user, ip, port]`) so notifications contain them.

## 3. Tests (first!)
- Add a scenario to `scenarios` in `server/internal/app/alerting/ruleset/defaults_test.go`: data that triggers the rule at
  the expected level and calm data, in the units the agent sends (ppm for ratios). `TestDefaults_EachRuleFiresOnItsData`
  fails for a rule without one; `TestDefaults_HaveHysteresisOrDuration` asks for `clear` and `for`.
- Loading (schema, catalog, file name) is covered by the same tests via `ruleset.Defaults()`.
- New rule *types* or evaluator behaviour: tests in `server/internal/app/alerting/types_test.go` and, for the state machine
  (pending, hysteresis, flapping), `server/internal/domain/alert/alert_test.go`.
- For `event` rules: test grouping (`groupBy`) and window counting incl. boundary.

## 4. Notifications & UI
- Check rendered notification text for all channel templates (golden files in `server/internal/adapters/notify/testdata/`).
- Rule appears in the rules list with help text; verify DE + EN.

## Checklist
- [ ] Typed rule, sensible `for`/`clear`, selector excludes noise
- [ ] Help DE + EN, `notifyFields` for events
- [ ] Schema + scenario tests green, golden notification files updated
- [ ] KONZEPT §12.3 table updated if it's a new default
