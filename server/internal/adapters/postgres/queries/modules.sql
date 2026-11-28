-- The modules of the agents (M5-06, ADR 0039). Every query runs in the organization's tenant
-- transaction.

-- name: ListModuleSettings :many
-- The settings of every group of the organization.
SELECT group_id, module_id, enabled, interval_seconds, config::text AS config
FROM module_settings
ORDER BY group_id, module_id;

-- name: ListGroupBuiltinModules :many
-- The modules the agents of each group's nodes have built in.
SELECT n.group_id, array_agg(DISTINCT m ORDER BY m)::text[] AS modules
FROM nodes n, unnest(n.modules) AS m
GROUP BY n.group_id;

-- name: UpsertModuleSetting :exec
INSERT INTO module_settings (org_id, group_id, module_id, enabled, interval_seconds, config, updated_at, updated_by)
VALUES (current_org_id(), sqlc.arg(group_id), sqlc.arg(module_id), sqlc.arg(enabled), sqlc.narg(interval_seconds),
        sqlc.arg(config)::jsonb, now(), sqlc.narg(updated_by))
ON CONFLICT (org_id, group_id, module_id) DO UPDATE
    SET enabled = EXCLUDED.enabled, interval_seconds = EXCLUDED.interval_seconds, config = EXCLUDED.config,
        updated_at = EXCLUDED.updated_at, updated_by = EXCLUDED.updated_by;

-- name: DeleteModuleSetting :execrows
DELETE FROM module_settings WHERE group_id = sqlc.arg(group_id) AND module_id = sqlc.arg(module_id);

-- name: ModuleSettingsForNode :many
-- The settings of a node's group (its configuration, ADR 0030).
SELECT s.module_id, s.enabled, s.interval_seconds, s.config::text AS config
FROM nodes n
JOIN module_settings s ON s.org_id = n.org_id AND s.group_id = n.group_id
WHERE n.id = sqlc.arg(node_id)
ORDER BY s.module_id;

-- name: SetNodeModules :one
-- Stores the modules the agent has built in; returns the list stored before.
UPDATE nodes n SET modules = sqlc.arg(modules)::text[]
FROM nodes old
WHERE n.id = sqlc.arg(node_id) AND old.id = n.id
RETURNING old.modules;

-- name: VisibleNodeModules :one
-- A node of the scope's groups with what it has built in.
SELECT id, group_id, modules, modules_vetoed FROM nodes
WHERE id = sqlc.arg(id) AND (sqlc.arg(all_groups)::boolean OR group_id = ANY (sqlc.arg(group_ids)::uuid[]));

-- name: DeleteModuleHealth :exec
DELETE FROM module_health WHERE node_id = sqlc.arg(node_id);

-- name: InsertModuleHealth :exec
INSERT INTO module_health (org_id, node_id, module_id, state, reason, last_success, collected_at, received_at)
VALUES (current_org_id(), sqlc.arg(node_id), sqlc.arg(module_id), sqlc.arg(state), sqlc.arg(reason),
        sqlc.narg(last_success), sqlc.arg(collected_at), sqlc.arg(received_at));

-- name: ModuleHealthOfNode :many
SELECT module_id, state, reason, last_success, collected_at, received_at
FROM module_health WHERE node_id = sqlc.arg(node_id)
ORDER BY module_id;

-- name: DeleteModuleHints :exec
DELETE FROM module_hints WHERE node_id = sqlc.arg(node_id);

-- name: InsertModuleHint :exec
INSERT INTO module_hints (org_id, node_id, module_id, reasons, suggested, collected_at, received_at)
VALUES (current_org_id(), sqlc.arg(node_id), sqlc.arg(module_id), sqlc.arg(reasons)::text[], sqlc.narg(suggested)::jsonb,
        sqlc.arg(collected_at), sqlc.arg(received_at));

-- name: ModuleHintsOfNode :many
-- The hints of a node without those dismissed for its group (V1-25).
SELECT h.module_id, h.reasons, coalesce(h.suggested::text, '')::text AS suggested, h.collected_at, h.received_at
FROM module_hints h
JOIN nodes n ON n.org_id = h.org_id AND n.id = h.node_id
WHERE h.node_id = sqlc.arg(node_id)
  AND NOT EXISTS (
      SELECT 1 FROM module_hint_dismissals d
      WHERE d.org_id = n.org_id AND d.group_id = n.group_id AND d.module_id = h.module_id
  )
ORDER BY h.module_id;

-- name: ListOpenModuleHints :many
-- The hints of the scope's nodes for modules their agent has built in and their group doesn't
-- have enabled ("nginx detected – enable the module?"), without those dismissed for the group
-- (V1-25).
SELECT h.node_id, n.hostname, n.group_id, h.module_id, h.reasons, coalesce(h.suggested::text, '')::text AS suggested,
       h.collected_at
FROM module_hints h
JOIN nodes n ON n.org_id = h.org_id AND n.id = h.node_id
WHERE (sqlc.arg(all_groups)::boolean OR n.group_id = ANY (sqlc.arg(group_ids)::uuid[]))
  AND h.module_id = ANY (n.modules)
  AND NOT EXISTS (
      SELECT 1 FROM module_settings s
      WHERE s.org_id = n.org_id AND s.group_id = n.group_id AND s.module_id = h.module_id AND s.enabled
  )
  AND NOT EXISTS (
      SELECT 1 FROM module_hint_dismissals d
      WHERE d.org_id = n.org_id AND d.group_id = n.group_id AND d.module_id = h.module_id
  )
ORDER BY n.hostname, n.id, h.module_id
LIMIT sqlc.arg(max_rows);

-- name: LockModuleSetting :exec
-- Serializes the changes of a group's module (V1-13): a change checks the credentials it keeps
-- in its transaction, and a removal in between takes them along.
SELECT pg_advisory_xact_lock(hashtextextended('modules:' || current_org_id()::text || ':' ||
                                              sqlc.arg(group_id)::uuid::text || ':' || sqlc.arg(module_id)::text, 0));

-- name: ModuleSecretFields :many
-- The credentials of a group's module that have a value (names only).
SELECT field FROM module_secrets
WHERE group_id = sqlc.arg(group_id) AND module_id = sqlc.arg(module_id)
ORDER BY field;

-- name: ListModuleSecretFields :many
-- The fields with a value of every module setting of the organization (names only).
SELECT group_id, module_id, field FROM module_secrets ORDER BY group_id, module_id, field;

-- name: UpsertModuleSecret :exec
INSERT INTO module_secrets (org_id, group_id, module_id, field, sealed, updated_at, updated_by)
VALUES (current_org_id(), sqlc.arg(group_id), sqlc.arg(module_id), sqlc.arg(field), sqlc.arg(sealed), now(),
        sqlc.narg(updated_by))
ON CONFLICT (org_id, group_id, module_id, field) DO UPDATE
    SET sealed = EXCLUDED.sealed, updated_at = EXCLUDED.updated_at, updated_by = EXCLUDED.updated_by;

-- name: DeleteModuleSecret :exec
DELETE FROM module_secrets
WHERE group_id = sqlc.arg(group_id) AND module_id = sqlc.arg(module_id) AND field = sqlc.arg(field);

-- name: ModuleSecretsForNode :many
-- The sealed credentials of a node's group (only the agent's session opens them).
SELECT s.group_id, s.module_id, s.field, s.sealed
FROM nodes n
JOIN module_secrets s ON s.org_id = n.org_id AND s.group_id = n.group_id
WHERE n.id = sqlc.arg(node_id)
ORDER BY s.module_id, s.field;

-- name: ListModuleHintDismissals :many
-- The dismissed suggestions of every group of the organization (V1-25).
SELECT group_id, module_id FROM module_hint_dismissals ORDER BY group_id, module_id;

-- name: InsertModuleHintDismissal :execrows
INSERT INTO module_hint_dismissals (org_id, group_id, module_id)
VALUES (current_org_id(), sqlc.arg(group_id), sqlc.arg(module_id))
ON CONFLICT (org_id, group_id, module_id) DO NOTHING;

-- name: DeleteModuleHintDismissal :execrows
DELETE FROM module_hint_dismissals WHERE group_id = sqlc.arg(group_id) AND module_id = sqlc.arg(module_id);
