-- Nodes. All queries run inside InTenant (RLS on org_id).

-- name: InsertNode :exec
INSERT INTO nodes (id, org_id, group_id, hostname, machine_id, os_id, os_version, kernel_release,
                   architecture, cpu_count, memory_bytes, enrolled_with)
VALUES (sqlc.arg(id), current_org_id(), sqlc.arg(group_id), sqlc.arg(hostname), sqlc.arg(machine_id),
        sqlc.arg(os_id), sqlc.arg(os_version), sqlc.arg(kernel_release), sqlc.arg(architecture),
        sqlc.arg(cpu_count), sqlc.arg(memory_bytes), sqlc.narg(enrolled_with));

-- name: GetNode :one
SELECT id, group_id, hostname, machine_id, os_id, os_version, kernel_release, architecture,
       cpu_count, memory_bytes, created_at
FROM nodes WHERE id = sqlc.arg(id);

-- name: UpdateNodeOnConnect :one
-- The agent's Hello: current host facts and version (the machine-id never changes here).
UPDATE nodes SET hostname = sqlc.arg(hostname), os_id = sqlc.arg(os_id), os_version = sqlc.arg(os_version),
                 kernel_release = sqlc.arg(kernel_release), architecture = sqlc.arg(architecture),
                 cpu_count = sqlc.arg(cpu_count), memory_bytes = sqlc.arg(memory_bytes),
                 agent_version = sqlc.arg(agent_version), last_seen_at = sqlc.arg(seen_at),
                 connected_at = sqlc.arg(seen_at), security_source = sqlc.arg(security_source)
WHERE nodes.id = sqlc.arg(id)
RETURNING group_id,
          (SELECT o.samples_per_second FROM organizations o WHERE o.id = nodes.org_id) AS samples_per_second,
          (SELECT o.series_per_node FROM organizations o WHERE o.id = nodes.org_id) AS series_per_node;

-- name: TouchNode :execrows
UPDATE nodes SET last_seen_at = GREATEST(COALESCE(last_seen_at, sqlc.arg(seen_at)), sqlc.arg(seen_at))
WHERE id = sqlc.arg(id);

-- name: ListVisibleNodes :many
-- Nodes of the scope's groups with their group name, for the UI (M1-26).
SELECT n.id, n.group_id, g.name AS group_name, n.hostname, n.os_id, n.os_version, n.kernel_release,
       n.architecture, n.cpu_count, n.memory_bytes, coalesce(n.agent_version, '')::text AS agent_version,
       n.created_at, n.last_seen_at, n.locked_at, coalesce(n.lock_reason, '')::text AS lock_reason,
       coalesce(n.last_ip, '')::text AS last_ip, n.takeover_until, n.tags, n.security_source, n.logs_vetoed,
       n.learning_since
FROM nodes n JOIN node_groups g ON g.org_id = n.org_id AND g.id = n.group_id
WHERE sqlc.arg(all_groups)::boolean OR n.group_id = ANY (sqlc.arg(group_ids)::uuid[])
ORDER BY n.hostname, n.id;

-- name: VisibleGroupExists :one
SELECT EXISTS (
    SELECT 1 FROM node_groups
    WHERE id = sqlc.arg(group_id) AND (sqlc.arg(all_groups)::boolean OR id = ANY (sqlc.arg(group_ids)::uuid[]))
);

-- Tags of nodes (M2-29). The node within the scope's groups, locked for the change.
-- name: NodeTagsForUpdate :one
SELECT group_id, tags FROM nodes
WHERE id = sqlc.arg(id) AND (sqlc.arg(all_groups)::boolean OR group_id = ANY (sqlc.arg(group_ids)::uuid[]))
FOR UPDATE;

-- name: SetNodeTags :exec
UPDATE nodes SET tags = sqlc.arg(tags) WHERE id = sqlc.arg(id);

-- name: VisibleNodeGroup :one
-- The group of one node of the scope's groups (the live view, M3-29).
SELECT group_id FROM nodes
WHERE id = sqlc.arg(id) AND (sqlc.arg(all_groups)::boolean OR group_id = ANY (sqlc.arg(group_ids)::uuid[]));
