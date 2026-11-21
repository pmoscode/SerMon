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
SELECT rule_id, node_id, dimension, policy_id, last_sent_at, first_sent_at, escalated, muted FROM alert_routing_state;

-- name: UpsertAlertRoutingState :exec
INSERT INTO alert_routing_state (org_id, rule_id, node_id, dimension, policy_id, last_sent_at, first_sent_at, escalated, muted)
VALUES (current_org_id(), sqlc.arg(rule_id), sqlc.arg(node_id), sqlc.arg(dimension), sqlc.arg(policy_id),
        sqlc.narg(last_sent_at), sqlc.narg(first_sent_at), sqlc.arg(escalated), sqlc.arg(muted))
ON CONFLICT (org_id, rule_id, node_id, dimension, policy_id)
    DO UPDATE SET last_sent_at = EXCLUDED.last_sent_at, first_sent_at = EXCLUDED.first_sent_at,
                  escalated = EXCLUDED.escalated, muted = EXCLUDED.muted;

-- name: DeleteAlertRoutingState :exec
DELETE FROM alert_routing_state
WHERE rule_id = sqlc.arg(rule_id) AND node_id = sqlc.arg(node_id) AND dimension = sqlc.arg(dimension)
  AND policy_id = sqlc.arg(policy_id);

-- name: ListAlertSilences :many
-- Silences that end after since (active, upcoming and recently ended ones).
SELECT id, matchers, node_ids, starts_at, ends_at, comment, created_at, created_by, expired_by
FROM alert_silences
WHERE ends_at > sqlc.arg(since)
ORDER BY starts_at, id;

-- name: GetAlertSilence :one
SELECT id, matchers, node_ids, starts_at, ends_at, comment, created_at, created_by, expired_by
FROM alert_silences
WHERE id = sqlc.arg(id);

-- name: CountOpenAlertSilences :one
SELECT count(*) FROM alert_silences WHERE ends_at > sqlc.arg(now);

-- name: InsertAlertSilence :exec
INSERT INTO alert_silences (org_id, id, matchers, node_ids, starts_at, ends_at, comment, created_at, created_by)
VALUES (current_org_id(), sqlc.arg(id), sqlc.arg(matchers), sqlc.arg(node_ids)::uuid[], sqlc.arg(starts_at),
        sqlc.arg(ends_at), sqlc.arg(comment), sqlc.arg(created_at), sqlc.narg(created_by));

-- name: ExpireAlertSilence :execrows
-- Ends a silence at at; one that has not begun yet is cancelled.
UPDATE alert_silences
SET ends_at = sqlc.arg(at), starts_at = LEAST(starts_at, sqlc.arg(at)), expired_by = sqlc.narg(expired_by)
WHERE id = sqlc.arg(id) AND ends_at > sqlc.arg(at);

-- name: PurgeAlertSilences :exec
DELETE FROM alert_silences WHERE ends_at < sqlc.arg(before);

-- name: ListMaintenanceWindows :many
SELECT id, name, enabled, group_ids, days, start_minute, duration_minutes, time_zone, created_at, updated_at, created_by
FROM maintenance_windows
ORDER BY lower(name), id;

-- name: InsertMaintenanceWindow :exec
INSERT INTO maintenance_windows (org_id, id, name, enabled, group_ids, days, start_minute, duration_minutes, time_zone,
                                 created_at, updated_at, created_by)
VALUES (current_org_id(), sqlc.arg(id), sqlc.arg(name), sqlc.arg(enabled), sqlc.arg(group_ids)::uuid[],
        sqlc.arg(days)::smallint[], sqlc.arg(start_minute), sqlc.arg(duration_minutes), sqlc.arg(time_zone),
        sqlc.arg(created_at), sqlc.arg(updated_at), sqlc.narg(created_by));

-- name: UpdateMaintenanceWindow :execrows
UPDATE maintenance_windows
SET name = sqlc.arg(name), enabled = sqlc.arg(enabled), group_ids = sqlc.arg(group_ids)::uuid[],
    days = sqlc.arg(days)::smallint[], start_minute = sqlc.arg(start_minute),
    duration_minutes = sqlc.arg(duration_minutes), time_zone = sqlc.arg(time_zone), updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id);

-- name: DeleteMaintenanceWindow :execrows
DELETE FROM maintenance_windows WHERE id = sqlc.arg(id);
