-- Log settings and redaction rules (M3-03, ADR 0030). Every query runs in the organization's
-- tenant transaction.

-- name: ListGroupLogSettings :many
SELECT g.id, g.name, ls.settings
FROM node_groups g
LEFT JOIN log_settings ls ON ls.org_id = g.org_id AND ls.group_id = g.id
ORDER BY g.name;

-- name: OrganizationAgentConfig :one
SELECT agent_config_version, redaction_rules, viewers_see_logs, live_hide_cmdline FROM organizations WHERE id = current_org_id();

-- name: SetLiveHideCmdline :exec
UPDATE organizations SET live_hide_cmdline = sqlc.arg(hide) WHERE id = current_org_id();

-- name: LiveHideCmdline :one
SELECT live_hide_cmdline FROM organizations WHERE id = current_org_id();

-- name: SetViewersSeeLogs :exec
UPDATE organizations SET viewers_see_logs = sqlc.arg(see) WHERE id = current_org_id();

-- name: ListNodeConfigStatus :many
-- What every agent reported about its configuration (M3-12).
SELECT id, hostname, group_id, config_version, coalesce(config_error, '')::text AS config_error, logs_vetoed,
       config_reported_at
FROM nodes
ORDER BY hostname, id;

-- name: NodeGroupExists :one
SELECT EXISTS (SELECT 1 FROM node_groups WHERE id = sqlc.arg(group_id));

-- name: UpsertGroupLogSettings :exec
INSERT INTO log_settings (org_id, group_id, settings, updated_at, updated_by)
VALUES (current_org_id(), sqlc.arg(group_id), sqlc.arg(settings), now(), sqlc.narg(updated_by))
ON CONFLICT (org_id, group_id) DO UPDATE
    SET settings = EXCLUDED.settings, updated_at = EXCLUDED.updated_at, updated_by = EXCLUDED.updated_by;

-- name: DeleteGroupLogSettings :exec
DELETE FROM log_settings WHERE group_id = sqlc.arg(group_id);

-- name: SetRedactionRules :exec
UPDATE organizations SET redaction_rules = sqlc.arg(rules) WHERE id = current_org_id();

-- name: BumpAgentConfigVersion :one
UPDATE organizations SET agent_config_version = agent_config_version + 1
WHERE id = current_org_id()
RETURNING agent_config_version;

-- name: AgentConfigForNode :one
-- The configuration of a node: its organization's version and rules, its group's settings (NULL:
-- the defaults).
SELECT o.agent_config_version, o.redaction_rules, ls.settings
FROM nodes n
JOIN organizations o ON o.id = n.org_id
LEFT JOIN log_settings ls ON ls.org_id = n.org_id AND ls.group_id = n.group_id
WHERE n.id = sqlc.arg(node_id);

-- name: RecordAgentConfigApplied :execrows
UPDATE nodes
SET config_version = sqlc.arg(version), config_error = sqlc.narg(error), logs_vetoed = sqlc.arg(logs_vetoed),
    modules_vetoed = sqlc.arg(modules_vetoed), config_reported_at = sqlc.arg(reported_at)
WHERE id = sqlc.arg(node_id);
