-- Alert rule overrides (M2-04, ADR 0007). Every query runs in the organization's tenant transaction.

-- name: ListAlertRuleOverrides :many
SELECT rule_id, disabled, document, source
FROM alert_rule_overrides
ORDER BY rule_id;

-- name: UpsertAlertRuleOverride :exec
INSERT INTO alert_rule_overrides (org_id, rule_id, disabled, document, source, updated_at, updated_by)
VALUES (current_org_id(), sqlc.arg(rule_id), sqlc.arg(disabled), sqlc.narg(document), sqlc.narg(source), now(),
        sqlc.narg(updated_by))
ON CONFLICT (org_id, rule_id) DO UPDATE SET
    disabled = EXCLUDED.disabled, document = EXCLUDED.document, source = EXCLUDED.source,
    updated_at = now(), updated_by = EXCLUDED.updated_by;

-- name: DeleteAlertRuleOverride :execrows
DELETE FROM alert_rule_overrides
WHERE rule_id = sqlc.arg(rule_id);
