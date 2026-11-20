-- Alert engine (M2-02, ADR 0007). Every query runs in the organization's tenant transaction.

-- name: LeaseAlertOrganization :execrows
-- Compare-and-swap: the holder renews, anybody else only takes over an expired lease.
INSERT INTO alert_leases (org_id, holder, expires_at)
VALUES (current_org_id(), sqlc.arg(holder), sqlc.arg(until))
ON CONFLICT (org_id) DO UPDATE SET holder = EXCLUDED.holder, expires_at = EXCLUDED.expires_at
WHERE alert_leases.holder = EXCLUDED.holder OR alert_leases.expires_at <= sqlc.arg(now);

-- name: ListAlertInstances :many
SELECT rule_id, node_id, dimension, state, since, pending_since, fired_at, ack_by, ack_at, flapping, value, flips
FROM alert_instances;

-- name: UpsertAlertInstance :exec
INSERT INTO alert_instances (org_id, rule_id, node_id, dimension, state, since, pending_since, fired_at,
                             ack_by, ack_at, flapping, value, flips, updated_at)
VALUES (current_org_id(), sqlc.arg(rule_id), sqlc.arg(node_id), sqlc.arg(dimension), sqlc.arg(state), sqlc.arg(since),
        sqlc.narg(pending_since), sqlc.narg(fired_at), sqlc.narg(ack_by), sqlc.narg(ack_at), sqlc.arg(flapping),
        sqlc.arg(value), sqlc.arg(flips)::timestamptz[], now())
ON CONFLICT (org_id, rule_id, node_id, dimension) DO UPDATE SET
    state = EXCLUDED.state, since = EXCLUDED.since, pending_since = EXCLUDED.pending_since,
    fired_at = EXCLUDED.fired_at, ack_by = EXCLUDED.ack_by, ack_at = EXCLUDED.ack_at,
    flapping = EXCLUDED.flapping, value = EXCLUDED.value, flips = EXCLUDED.flips, updated_at = now();

-- name: DeleteAlertInstance :exec
DELETE FROM alert_instances
WHERE rule_id = sqlc.arg(rule_id) AND node_id = sqlc.arg(node_id) AND dimension = sqlc.arg(dimension);

-- name: InsertAlertHistory :exec
-- One row per transition (COPY doesn't work with row-level security; a round has few).
INSERT INTO alert_history (org_id, rule_id, node_id, dimension, kind, from_state, to_state, at, value, by_user)
VALUES (current_org_id(), sqlc.arg(rule_id), sqlc.arg(node_id), sqlc.arg(dimension), sqlc.arg(kind),
        sqlc.arg(from_state), sqlc.arg(to_state), sqlc.arg(at), sqlc.arg(value), sqlc.narg(by_user));

-- name: AlertInstanceHistory :many
SELECT kind, from_state, to_state, at, value, by_user
FROM alert_history
WHERE rule_id = sqlc.arg(rule_id) AND node_id = sqlc.arg(node_id) AND dimension = sqlc.arg(dimension)
ORDER BY at DESC, id DESC
LIMIT sqlc.arg(max_entries);
