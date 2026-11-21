-- Notification policies and the routing state (M2-14, ADR 0007). Every query runs in the
-- organization's tenant transaction.

-- name: ListNotificationPolicies :many
SELECT id, name, enabled, matchers, grouping, repeat_seconds, escalation_seconds, created_at, updated_at, created_by
FROM notification_policies
ORDER BY lower(name), id;

-- name: ListNotificationPolicyChannels :many
SELECT policy_id, channel_id, escalation
FROM notification_policy_channels
ORDER BY policy_id, escalation, position;

-- name: InsertNotificationPolicy :exec
INSERT INTO notification_policies (org_id, id, name, enabled, matchers, grouping, repeat_seconds, escalation_seconds,
                                   created_at, updated_at, created_by)
VALUES (current_org_id(), sqlc.arg(id), sqlc.arg(name), sqlc.arg(enabled), sqlc.arg(matchers), sqlc.arg(grouping),
        sqlc.arg(repeat_seconds), sqlc.narg(escalation_seconds), sqlc.arg(created_at), sqlc.arg(updated_at),
        sqlc.narg(created_by));

-- name: UpdateNotificationPolicy :execrows
UPDATE notification_policies
SET name = sqlc.arg(name), enabled = sqlc.arg(enabled), matchers = sqlc.arg(matchers), grouping = sqlc.arg(grouping),
    repeat_seconds = sqlc.arg(repeat_seconds), escalation_seconds = sqlc.narg(escalation_seconds),
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id);

-- name: DeleteNotificationPolicy :execrows
DELETE FROM notification_policies WHERE id = sqlc.arg(id);

-- name: DeleteNotificationPolicyChannels :exec
DELETE FROM notification_policy_channels WHERE policy_id = sqlc.arg(policy_id);

-- name: InsertNotificationPolicyChannel :exec
INSERT INTO notification_policy_channels (org_id, policy_id, channel_id, escalation, position)
VALUES (current_org_id(), sqlc.arg(policy_id), sqlc.arg(channel_id), sqlc.arg(escalation), sqlc.arg(position));

-- name: ListEnabledNotificationChannelIDs :many
SELECT id FROM notification_channels WHERE enabled ORDER BY created_at, id;

-- name: ListAlertRoutingStates :many
SELECT rule_id, node_id, dimension, policy_id, last_sent_at, escalated FROM alert_routing_state;

-- name: UpsertAlertRoutingState :exec
INSERT INTO alert_routing_state (org_id, rule_id, node_id, dimension, policy_id, last_sent_at, escalated)
VALUES (current_org_id(), sqlc.arg(rule_id), sqlc.arg(node_id), sqlc.arg(dimension), sqlc.arg(policy_id),
        sqlc.arg(last_sent_at), sqlc.arg(escalated))
ON CONFLICT (org_id, rule_id, node_id, dimension, policy_id)
    DO UPDATE SET last_sent_at = EXCLUDED.last_sent_at, escalated = EXCLUDED.escalated;

-- name: DeleteAlertRoutingState :exec
DELETE FROM alert_routing_state
WHERE rule_id = sqlc.arg(rule_id) AND node_id = sqlc.arg(node_id) AND dimension = sqlc.arg(dimension)
  AND policy_id = sqlc.arg(policy_id);
