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
