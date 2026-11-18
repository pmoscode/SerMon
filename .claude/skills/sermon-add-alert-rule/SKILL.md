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
File: `rules/<category>/<id>.yaml` (`id` = `<category>.<subject>.<condition>`, e.g. `system.disk.inodes_high`).

```yaml
apiVersion: sermon/v1
kind: AlertRule
metadata:
  id: system.disk.inodes_high
  name: { de: "Inodes fast erschöpft", en: "Inodes almost exhausted" }
  labels: { category: disk }
spec:
  type: threshold
  metric: sermon_filesystem_inodes_used_ratio
  dimensions: [mountpoint]
  aggregate: max
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
- Exclude irrelevant dimensions (tmpfs, squashfs, overlay, loop devices) via selector.
- `help` is mandatory in DE and EN: meaning, typical causes, concrete next steps (link to Live view / Logs where useful).
- For security events specify `notifyFields` (e.g. `[ts, user, ip, port]`) so notifications contain them.

## 3. Tests (first!)
- Schema validation test picks the file up automatically (`server/internal/alerting/rules_test.go` loads all `rules/**`). Run it.
- Add an engine scenario test in `server/internal/alerting/scenarios/` using the fake clock and a synthetic series/event stream:
  - stays OK below threshold
  - goes PENDING → WARNING/CRITICAL after `for`
  - resolves only below `clear` (hysteresis)
  - flapping is detected
  - silence / maintenance window suppresses notifications
- For `event` rules: test grouping (`groupBy`) and window counting incl. boundary.

## 4. Notifications & UI
- Check rendered notification text for all channel templates (golden files in `server/internal/adapters/notify/testdata/`).
- Rule appears in the rules list with help text; verify DE + EN.

## Checklist
- [ ] Typed rule, sensible `for`/`clear`, selector excludes noise
- [ ] Help DE + EN, `notifyFields` for events
- [ ] Schema + scenario tests green, golden notification files updated
- [ ] KONZEPT §12.3 table updated if it's a new default
