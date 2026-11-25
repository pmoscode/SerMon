-- Updates of the nodes (M4-09). Writes run in the tenant of the node's organization, reads within
-- a scope: the scope's groups are those of the node now.

-- name: CountOtherNodeUpdatePackages :one
-- The packages stored for the organization's other nodes (its limit).
SELECT count(*) FROM node_update_packages WHERE node_id <> sqlc.arg(node_id);

-- name: UpsertNodeUpdateState :one
-- Makes the report the node's current one unless the stored one was collected at the same time or
-- later; returns nothing then.
INSERT INTO node_update_state AS s (
    org_id, node_id, collected_at, received_at, availability, reason, package_manager, total_count,
    security_count, packages_stored, reboot_required, reboot_reason, last_upgrade_at,
    allow_remote_updates, allow_remote_reboot, allow_self_update, refreshed, reboot_required_since)
VALUES (
    current_org_id(), sqlc.arg(node_id), sqlc.arg(collected_at), sqlc.arg(received_at), sqlc.arg(availability),
    sqlc.arg(reason), sqlc.arg(package_manager), sqlc.arg(total_count), sqlc.arg(security_count),
    sqlc.arg(packages_stored), sqlc.arg(reboot_required), sqlc.arg(reboot_reason), sqlc.narg(last_upgrade_at),
    sqlc.narg(allow_remote_updates), sqlc.narg(allow_remote_reboot), sqlc.narg(allow_self_update), sqlc.arg(refreshed),
    CASE WHEN sqlc.arg(reboot_required)::boolean THEN sqlc.arg(collected_at)::timestamptz END)
ON CONFLICT (org_id, node_id) DO UPDATE SET
    collected_at = excluded.collected_at, received_at = excluded.received_at,
    availability = excluded.availability, reason = excluded.reason,
    package_manager = excluded.package_manager, total_count = excluded.total_count,
    security_count = excluded.security_count, packages_stored = excluded.packages_stored,
    reboot_required = excluded.reboot_required, reboot_reason = excluded.reboot_reason,
    last_upgrade_at = excluded.last_upgrade_at, allow_remote_updates = excluded.allow_remote_updates,
    allow_remote_reboot = excluded.allow_remote_reboot, allow_self_update = excluded.allow_self_update,
    refreshed = excluded.refreshed,
    -- Since the first report that needed the reboot (M4-16).
    reboot_required_since = CASE WHEN excluded.reboot_required THEN coalesce(s.reboot_required_since, excluded.collected_at) END
WHERE s.collected_at < excluded.collected_at
RETURNING s.node_id;

-- name: DeleteGoneNodeUpdatePackages :exec
-- Deletes the node's packages the new report doesn't list.
DELETE FROM node_update_packages p
WHERE p.node_id = sqlc.arg(node_id)
  AND NOT EXISTS (
      SELECT 1 FROM (SELECT unnest(sqlc.arg(names)::text[]) AS name, unnest(sqlc.arg(candidates)::text[]) AS candidate) t
      WHERE t.name = p.name AND t.candidate = p.candidate_version);

-- name: UpsertNodeUpdatePackages :exec
-- Stores the listed packages; one listed before keeps its first_seen_at.
INSERT INTO node_update_packages AS p (
    org_id, node_id, name, candidate_version, installed_version, source, security, first_seen_at)
SELECT current_org_id(), sqlc.arg(node_id), t.name, t.candidate, t.installed, t.source, t.security, sqlc.arg(seen_at)
FROM (SELECT unnest(sqlc.arg(names)::text[]) AS name, unnest(sqlc.arg(candidates)::text[]) AS candidate,
             unnest(sqlc.arg(installed)::text[]) AS installed, unnest(sqlc.arg(sources)::text[]) AS source,
             unnest(sqlc.arg(security)::boolean[]) AS security) t
ON CONFLICT (org_id, node_id, name, candidate_version) DO UPDATE SET
    installed_version = excluded.installed_version, source = excluded.source, security = excluded.security;

-- name: SetOldestSecurityUpdate :exec
UPDATE node_update_state SET oldest_security_at = (
    SELECT min(p.first_seen_at) FROM node_update_packages p WHERE p.node_id = sqlc.arg(node_id) AND p.security)
WHERE node_id = sqlc.arg(node_id);

-- name: GetNodeUpdates :one
-- A node of the scope with its report; no report: the report's columns are NULL.
SELECT n.id, n.hostname, n.group_id, s.collected_at, s.received_at, s.availability, s.reason,
       s.package_manager, s.total_count, s.security_count, s.packages_stored, s.reboot_required,
       s.reboot_reason, s.last_upgrade_at, s.allow_remote_updates, s.allow_remote_reboot,
       s.allow_self_update, s.refreshed, s.oldest_security_at,
       coalesce(n.agent_version, '')::text AS agent_version, n.install_method, n.architecture
FROM nodes n LEFT JOIN node_update_state s ON s.org_id = n.org_id AND s.node_id = n.id
WHERE n.id = sqlc.arg(node_id)
  AND (sqlc.arg(all_groups)::boolean OR n.group_id = ANY (sqlc.arg(group_ids)::uuid[]));

-- name: ListNodeUpdatePackages :many
SELECT name, candidate_version, installed_version, source, security, first_seen_at
FROM node_update_packages WHERE node_id = sqlc.arg(node_id)
ORDER BY security DESC, name, candidate_version;

-- name: ListFleetUpdates :many
-- The nodes of the scope with their reports, without packages.
SELECT n.id, n.hostname, n.group_id, s.collected_at, s.received_at, s.availability, s.reason,
       s.package_manager, s.total_count, s.security_count, s.packages_stored, s.reboot_required,
       s.reboot_reason, s.last_upgrade_at, s.allow_remote_updates, s.allow_remote_reboot,
       s.allow_self_update, s.refreshed, s.oldest_security_at,
       coalesce(n.agent_version, '')::text AS agent_version, n.install_method, n.architecture
FROM nodes n LEFT JOIN node_update_state s ON s.org_id = n.org_id AND s.node_id = n.id
WHERE sqlc.arg(all_groups)::boolean OR n.group_id = ANY (sqlc.arg(group_ids)::uuid[])
ORDER BY n.hostname, n.id;

-- Update policies (M4-10).

-- name: GetUpdatePolicy :one
-- A group of the scope with its policy; without one the policy's columns are NULL.
SELECT g.id, g.name, p.mode, p.window_days, p.window_start_minute, p.window_minutes, p.window_time_zone,
       p.auto_reboot, p.max_parallel, p.stop_on_failure, p.health_check_minutes, p.updated_at, p.updated_by,
       p.agent_update, p.agent_channel
FROM node_groups g LEFT JOIN update_policies p ON p.org_id = g.org_id AND p.group_id = g.id
WHERE g.id = sqlc.arg(group_id)
  AND (sqlc.arg(all_groups)::boolean OR g.id = ANY (sqlc.arg(group_ids)::uuid[]));

-- name: ListUpdatePolicies :many
SELECT g.id, g.name, p.mode, p.window_days, p.window_start_minute, p.window_minutes, p.window_time_zone,
       p.auto_reboot, p.max_parallel, p.stop_on_failure, p.health_check_minutes, p.updated_at, p.updated_by,
       p.agent_update, p.agent_channel
FROM node_groups g LEFT JOIN update_policies p ON p.org_id = g.org_id AND p.group_id = g.id
WHERE sqlc.arg(all_groups)::boolean OR g.id = ANY (sqlc.arg(group_ids)::uuid[])
ORDER BY g.name, g.id;

-- name: UpsertUpdatePolicy :exec
INSERT INTO update_policies (
    org_id, group_id, mode, window_days, window_start_minute, window_minutes, window_time_zone,
    auto_reboot, max_parallel, stop_on_failure, health_check_minutes, updated_at, updated_by,
    agent_update, agent_channel)
VALUES (
    current_org_id(), sqlc.arg(group_id), sqlc.arg(mode), sqlc.narg(window_days)::smallint[], sqlc.narg(window_start_minute),
    sqlc.narg(window_minutes), sqlc.narg(window_time_zone), sqlc.arg(auto_reboot), sqlc.arg(max_parallel),
    sqlc.arg(stop_on_failure), sqlc.arg(health_check_minutes), sqlc.arg(updated_at), sqlc.narg(updated_by),
    sqlc.arg(agent_update), sqlc.arg(agent_channel))
ON CONFLICT (org_id, group_id) DO UPDATE SET
    mode = excluded.mode, window_days = excluded.window_days, window_start_minute = excluded.window_start_minute,
    window_minutes = excluded.window_minutes, window_time_zone = excluded.window_time_zone,
    auto_reboot = excluded.auto_reboot, max_parallel = excluded.max_parallel,
    stop_on_failure = excluded.stop_on_failure, health_check_minutes = excluded.health_check_minutes,
    updated_at = excluded.updated_at, updated_by = excluded.updated_by,
    agent_update = excluded.agent_update, agent_channel = excluded.agent_channel;

-- name: DeleteUpdatePolicy :exec
DELETE FROM update_policies WHERE group_id = sqlc.arg(group_id);

-- name: GetNodeUpdateGauge :one
-- What the derived series of the node's updates show (M4-16).
SELECT availability, total_count, security_count, oldest_security_at, reboot_required, reboot_required_since
FROM node_update_state WHERE node_id = sqlc.arg(node_id);
