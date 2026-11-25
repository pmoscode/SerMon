-- The update orchestrator (M4-13), always in the tenant of one organization.

-- name: LeaseUpdateOrganization :execrows
-- Compare-and-swap: the holder renews, anybody else only takes over an expired lease.
INSERT INTO update_leases (org_id, holder, expires_at)
VALUES (current_org_id(), sqlc.arg(holder), sqlc.arg(until))
ON CONFLICT (org_id) DO UPDATE SET holder = EXCLUDED.holder, expires_at = EXCLUDED.expires_at
WHERE update_leases.holder = EXCLUDED.holder OR update_leases.expires_at <= sqlc.arg(now);

-- name: HoldUpdateLease :execrows
-- Fencing: the orchestrator saves only while it holds the lease; the row lock makes a takeover
-- wait for the save to end.
SELECT 1 FROM update_leases WHERE holder = sqlc.arg(holder) AND expires_at > sqlc.arg(now) FOR UPDATE;

-- name: LatestUpdateRollouts :many
-- The last scheduled rollout of each group.
SELECT DISTINCT ON (group_id) id, group_id, window_start, window_end, state, reason
FROM update_rollouts WHERE NOT manual ORDER BY group_id, window_start DESC;

-- name: RunningManualRollouts :many
-- The rollouts by hand that still run (M4-14).
SELECT id, group_id, window_start, window_end, state, reason, mode, auto_reboot
FROM update_rollouts WHERE manual AND state = 1 ORDER BY window_start, id;

-- name: ListUpdateRolloutNodes :many
SELECT rollout_id, node_id, hostname, state, reason, update_run, reboot_run, since, deadline, agent_run
FROM update_rollout_nodes WHERE rollout_id = ANY (sqlc.arg(rollout_ids)::uuid[])
ORDER BY rollout_id, position;

-- name: ListRolloutNodeViews :many
-- The nodes with what a rollout needs: online, their session, their report and whether a
-- critical alert other than offline or rebooted fires.
SELECT n.id, n.group_id, n.hostname, n.last_seen_at, n.connected_at,
       coalesce(n.agent_version, '')::text AS agent_version, n.install_method, n.architecture,
       s.received_at, s.availability, s.package_manager, s.total_count, s.security_count, s.reboot_required,
       s.allow_remote_updates, s.allow_remote_reboot, s.allow_self_update,
       EXISTS (SELECT 1 FROM alert_instances a
               WHERE a.node_id = n.id AND a.state = 3
                 AND a.rule_id NOT IN ('system.node.offline', 'system.node.rebooted'))::boolean AS critical
FROM nodes n LEFT JOIN node_update_state s ON s.org_id = n.org_id AND s.node_id = n.id
ORDER BY n.hostname, n.id;

-- name: ListNodeCommandsByID :many
SELECT id, node_id, kind, scope, reason, version, state, created_at, expires_at, created_by, sent_at,
       phase, exit_code, result_reason, started_at, ended_at
FROM node_commands WHERE id = ANY (sqlc.arg(ids)::uuid[]);

-- name: UpsertUpdateRollout :exec
INSERT INTO update_rollouts (org_id, id, group_id, window_start, window_end, state, reason, created_at, updated_at,
                             manual, mode, auto_reboot, created_by)
VALUES (current_org_id(), sqlc.arg(id), sqlc.arg(group_id), sqlc.arg(window_start), sqlc.arg(window_end),
        sqlc.arg(state), sqlc.arg(reason), sqlc.arg(now), sqlc.arg(now),
        sqlc.arg(manual), sqlc.narg(mode), sqlc.arg(auto_reboot), sqlc.narg(created_by))
ON CONFLICT (org_id, id) DO UPDATE SET state = excluded.state, reason = excluded.reason, updated_at = excluded.updated_at;

-- name: DeleteUpdateRolloutNodes :exec
DELETE FROM update_rollout_nodes WHERE rollout_id = sqlc.arg(rollout_id);

-- name: InsertUpdateRolloutNode :exec
INSERT INTO update_rollout_nodes (org_id, rollout_id, node_id, position, hostname, state, reason, update_run, reboot_run, since, deadline, agent_run)
VALUES (current_org_id(), sqlc.arg(rollout_id), sqlc.arg(node_id), sqlc.arg(position), sqlc.arg(hostname), sqlc.arg(state),
        sqlc.arg(reason), sqlc.narg(update_run), sqlc.narg(reboot_run), sqlc.arg(since), sqlc.narg(deadline), sqlc.narg(agent_run));

-- name: PruneNodeCommands :exec
DELETE FROM node_commands WHERE created_at < sqlc.arg(before);

-- name: PruneUpdateRollouts :exec
DELETE FROM update_rollouts WHERE created_at < sqlc.arg(before);

-- name: ListRecentRollouts :many
-- The latest rollouts of the scope's groups (M4-15), newest first.
SELECT r.id, r.group_id, g.name AS group_name, r.window_start, r.window_end, r.state, r.reason, r.manual,
       r.mode, r.auto_reboot, r.created_by, r.created_at
FROM update_rollouts r JOIN node_groups g ON g.org_id = r.org_id AND g.id = r.group_id
WHERE sqlc.arg(all_groups)::boolean OR r.group_id = ANY (sqlc.arg(group_ids)::uuid[])
ORDER BY r.created_at DESC, r.id DESC LIMIT sqlc.arg(max_rows);
