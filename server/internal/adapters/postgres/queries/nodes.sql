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
                 agent_version = sqlc.arg(agent_version), last_seen_at = sqlc.arg(seen_at)
WHERE id = sqlc.arg(id)
RETURNING group_id;

-- name: TouchNode :execrows
UPDATE nodes SET last_seen_at = GREATEST(COALESCE(last_seen_at, sqlc.arg(seen_at)), sqlc.arg(seen_at))
WHERE id = sqlc.arg(id);
