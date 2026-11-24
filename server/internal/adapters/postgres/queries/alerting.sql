-- Alert engine (M2-02, ADR 0007). Every query runs in the organization's tenant transaction.

-- name: LeaseAlertOrganization :execrows
-- Compare-and-swap: the holder renews, anybody else only takes over an expired lease.
INSERT INTO alert_leases (org_id, holder, expires_at)
VALUES (current_org_id(), sqlc.arg(holder), sqlc.arg(until))
ON CONFLICT (org_id) DO UPDATE SET holder = EXCLUDED.holder, expires_at = EXCLUDED.expires_at
WHERE alert_leases.holder = EXCLUDED.holder OR alert_leases.expires_at <= sqlc.arg(now);

-- name: HoldAlertLease :execrows
-- Fencing (M2-38): the engine saves only while it holds the lease; the row lock makes a takeover
-- wait for the save to end.
SELECT 1 FROM alert_leases
WHERE holder = sqlc.arg(holder) AND expires_at > sqlc.arg(now)
FOR UPDATE;

-- name: ListAlertInstances :many
SELECT rule_id, node_id, dimension, state, since, pending_since, fired_at, ack_by, ack_at, flapping, value, flips, fields
FROM alert_instances;

-- name: UpsertAlertInstance :exec
INSERT INTO alert_instances (org_id, rule_id, node_id, dimension, state, since, pending_since, fired_at,
                             ack_by, ack_at, flapping, value, flips, fields, updated_at)
VALUES (current_org_id(), sqlc.arg(rule_id), sqlc.arg(node_id), sqlc.arg(dimension), sqlc.arg(state), sqlc.arg(since),
        sqlc.narg(pending_since), sqlc.narg(fired_at), sqlc.narg(ack_by), sqlc.narg(ack_at), sqlc.arg(flapping),
        sqlc.arg(value), sqlc.arg(flips)::timestamptz[], sqlc.arg(fields), now())
ON CONFLICT (org_id, rule_id, node_id, dimension) DO UPDATE SET
    state = EXCLUDED.state, since = EXCLUDED.since, pending_since = EXCLUDED.pending_since,
    fired_at = EXCLUDED.fired_at,
    -- An acknowledgement written by the API while the engine evaluated survives the engine's
    -- save: kept while the alert fires on (same fired_at) and did not escalate to critical.
    ack_by = CASE WHEN EXCLUDED.ack_by IS NULL AND EXCLUDED.fired_at = alert_instances.fired_at
                       AND NOT (alert_instances.state = 2 AND EXCLUDED.state = 3)
                  THEN alert_instances.ack_by ELSE EXCLUDED.ack_by END,
    ack_at = CASE WHEN EXCLUDED.ack_by IS NULL AND EXCLUDED.fired_at = alert_instances.fired_at
                       AND NOT (alert_instances.state = 2 AND EXCLUDED.state = 3)
                  THEN alert_instances.ack_at ELSE EXCLUDED.ack_at END,
    flapping = EXCLUDED.flapping, value = EXCLUDED.value, flips = EXCLUDED.flips, fields = EXCLUDED.fields,
    updated_at = now();

-- name: DeleteAlertInstance :exec
DELETE FROM alert_instances
WHERE rule_id = sqlc.arg(rule_id) AND node_id = sqlc.arg(node_id) AND dimension = sqlc.arg(dimension);

-- name: InsertAlertHistory :exec
-- One row per transition (COPY doesn't work with row-level security; a round has few).
INSERT INTO alert_history (org_id, rule_id, node_id, dimension, kind, from_state, to_state, at, value, by_user, fields)
VALUES (current_org_id(), sqlc.arg(rule_id), sqlc.arg(node_id), sqlc.arg(dimension), sqlc.arg(kind),
        sqlc.arg(from_state), sqlc.arg(to_state), sqlc.arg(at), sqlc.arg(value), sqlc.narg(by_user), sqlc.arg(fields));

-- name: AlertInstanceHistory :many
SELECT kind, from_state, to_state, at, value, by_user, fields
FROM alert_history
WHERE rule_id = sqlc.arg(rule_id) AND node_id = sqlc.arg(node_id) AND dimension = sqlc.arg(dimension)
ORDER BY at DESC, id DESC
LIMIT sqlc.arg(max_entries);

-- name: AcknowledgeAlertInstance :execrows
-- Only a firing alert that nobody acknowledged yet (the engine may have changed it meanwhile).
UPDATE alert_instances
SET ack_by = sqlc.arg(ack_by), ack_at = sqlc.arg(ack_at), updated_at = now()
WHERE rule_id = sqlc.arg(rule_id) AND node_id = sqlc.arg(node_id) AND dimension = sqlc.arg(dimension)
  AND state IN (2, 3) AND ack_by IS NULL;

-- name: PurgeAlertHistory :execrows
DELETE FROM alert_history WHERE at < sqlc.arg(before);

-- name: AlertHistorySince :many
-- The statistics (M3-19): the transitions of the organization from since on, oldest first,
-- without their fields.
SELECT rule_id, node_id, dimension, kind, from_state, to_state, at, value
FROM alert_history
WHERE at >= sqlc.arg(since)
ORDER BY at, id
LIMIT sqlc.arg(max_entries);
