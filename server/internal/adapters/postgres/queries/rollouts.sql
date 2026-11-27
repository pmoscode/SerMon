-- The update orchestrator (M4-13), always in the tenant of one organization.

-- name: LeaseUpdateOrganization :execrows
-- Compare-and-swap: the holder renews, anybody else only takes over an expired lease. With the
-- database's time (M4-31): the instances' clocks may differ.
INSERT INTO update_leases (org_id, holder, expires_at)
VALUES (current_org_id(), sqlc.arg(holder), now() + make_interval(secs => sqlc.arg(lease_seconds)::float8))
ON CONFLICT (org_id) DO UPDATE SET holder = EXCLUDED.holder, expires_at = EXCLUDED.expires_at
WHERE update_leases.holder = EXCLUDED.holder OR update_leases.expires_at <= now();

-- name: ReleaseUpdateOrganization :exec
-- The holder gives the organization up: another instance takes it in its next round.
UPDATE update_leases SET expires_at = now()
WHERE org_id = current_org_id() AND holder = sqlc.arg(holder) AND expires_at > now();

-- name: HoldUpdateLease :execrows
-- Fencing: the orchestrator saves only while it holds the lease; the row lock makes a takeover
-- wait for the save to end.
SELECT 1 FROM update_leases WHERE holder = sqlc.arg(holder) AND expires_at > now() FOR UPDATE;

-- name: LockUpdates :exec
-- A cancel or a pause by hand waits for a save of the orchestrator in flight, and the next save
-- sees it (M4-31); without a lease yet no orchestrator plans the organization.
SELECT 1 FROM update_leases WHERE org_id = current_org_id() FOR UPDATE;

-- name: CancelledRollouts :many
-- Of the rollouts those cancelled by hand.
SELECT id FROM update_rollouts WHERE id = ANY (sqlc.arg(ids)::uuid[]) AND cancelled_at IS NOT NULL;

-- name: GetUpdateRollout :one
SELECT id, group_id, state, manual FROM update_rollouts WHERE id = sqlc.arg(id);

-- name: CancelUpdateRollout :one
-- Stops a running rollout by hand (M4-31).
UPDATE update_rollouts SET state = 3, reason = sqlc.arg(reason), cancelled_at = sqlc.arg(at), cancelled_by = sqlc.narg(cancelled_by), updated_at = sqlc.arg(at)
WHERE id = sqlc.arg(id) AND state = 1
RETURNING group_id;

-- name: SkipWaitingRolloutNodes :exec
UPDATE update_rollout_nodes SET state = 7, reason = sqlc.arg(reason), since = sqlc.arg(at)
WHERE rollout_id = sqlc.arg(rollout_id) AND state = 1;

-- name: CancelRolloutRuns :many
-- The runs of the rollout's nodes not started yet: pending ones are cancelled, sent ones withdrawn.
UPDATE node_commands SET
    state = CASE WHEN state = 1 THEN 6 ELSE state END, cancelled_at = sqlc.arg(at), cancelled_by = sqlc.narg(cancelled_by)
WHERE id IN (SELECT unnest(ARRAY[n.update_run, n.reboot_run, n.agent_run]) FROM update_rollout_nodes n
             WHERE n.rollout_id = sqlc.arg(rollout_id))
  AND cancelled_at IS NULL AND phase IS NULL AND state IN (1, 2)
RETURNING node_id, state;

-- name: SetUpdatesPaused :exec
UPDATE organizations SET updates_paused_at = sqlc.narg(paused_at), updates_paused_by = sqlc.narg(paused_by)
WHERE id = current_org_id();

-- name: LatestUpdateRollouts :many
-- The last scheduled rollout of each group.
SELECT DISTINCT ON (group_id) id, group_id, window_start, window_end, state, reason, cancelled_at
FROM update_rollouts WHERE NOT manual ORDER BY group_id, window_start DESC;

-- name: RunningManualRollouts :many
-- The rollouts by hand that still run (M4-14), and stopped ones with nodes in a run: they are
-- followed to the end of those (M4-31).
SELECT r.id, r.group_id, r.window_start, r.window_end, r.state, r.reason, r.mode, r.auto_reboot, r.cancelled_at
FROM update_rollouts r
WHERE r.manual AND (r.state = 1 OR (r.state = 3 AND EXISTS (
    SELECT 1 FROM update_rollout_nodes n
    WHERE n.org_id = r.org_id AND n.rollout_id = r.id AND n.state IN (2, 3, 4, 8))))
ORDER BY r.window_start, r.id;

-- name: EndRolloutSilences :execrows
-- The silences a rollout set for the node end at at: its part of the rollout ended (M4-31).
UPDATE alert_silences SET ends_at = sqlc.arg(at), starts_at = LEAST(starts_at, sqlc.arg(at))
WHERE node_ids = ARRAY[sqlc.arg(node_id)::uuid] AND created_by IS NULL AND comment = sqlc.arg(comment)
  AND ends_at > sqlc.arg(at);

-- name: ListUpdateRolloutNodes :many
SELECT rollout_id, node_id, hostname, state, reason, update_run, reboot_run, since, deadline, agent_run
FROM update_rollout_nodes WHERE rollout_id = ANY (sqlc.arg(rollout_ids)::uuid[])
ORDER BY rollout_id, position;

-- name: ListScopedRolloutNodes :many
-- The nodes of the rollouts the scope may still view: one that moved to another group leaves the
-- history of its old group for those who can't view the new one (M4-31).
SELECT rn.rollout_id, rn.node_id, rn.hostname, rn.state, rn.reason, rn.update_run, rn.reboot_run, rn.since, rn.deadline, rn.agent_run
FROM update_rollout_nodes rn JOIN nodes n ON n.org_id = rn.org_id AND n.id = rn.node_id
WHERE rn.rollout_id = ANY (sqlc.arg(rollout_ids)::uuid[])
  AND (sqlc.arg(all_groups)::boolean OR n.group_id = ANY (sqlc.arg(group_ids)::uuid[]))
ORDER BY rn.rollout_id, rn.position;

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
       phase, exit_code, result_reason, started_at, ended_at, cancelled_at
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
