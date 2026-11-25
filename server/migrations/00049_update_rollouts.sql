-- Rollouts of the update policies (M4-13, KONZEPT §14.2): the orchestrator of one instance per
-- organization (update_leases, like the alert engine's) starts a rollout when a window of a
-- group's policy begins and moves its nodes through update, reboot and health check.
--
-- A node is back after a reboot once a session began after it: nodes.connected_at, set at every
-- Hello (boot ID and uptime don't change in containers). The rollout writes server events of its
-- own (updates.run_failed, updates.rollout_stopped) next to the agents' security events, like the
-- server's other events (ADR 0027).

-- +goose Up
ALTER TABLE nodes ADD COLUMN connected_at timestamptz;

CREATE TABLE update_leases (
    org_id     uuid        PRIMARY KEY REFERENCES organizations (id) ON DELETE CASCADE,
    holder     text        NOT NULL CHECK (char_length(holder) BETWEEN 1 AND 64),
    expires_at timestamptz NOT NULL
);
ALTER TABLE update_leases ENABLE ROW LEVEL SECURITY;
ALTER TABLE update_leases FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON update_leases
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT, UPDATE ON update_leases TO sermon_app;

-- state: 1 running, 2 done, 3 stopped.
CREATE TABLE update_rollouts (
    org_id       uuid        NOT NULL,
    id           uuid        NOT NULL,
    group_id     uuid        NOT NULL,
    window_start timestamptz NOT NULL,
    window_end   timestamptz NOT NULL CHECK (window_end > window_start),
    state        smallint    NOT NULL CHECK (state BETWEEN 1 AND 3),
    reason       text        NOT NULL DEFAULT '' CHECK (octet_length(reason) <= 1024),
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    PRIMARY KEY (org_id, id),
    UNIQUE (org_id, group_id, window_start),
    FOREIGN KEY (org_id, group_id) REFERENCES node_groups (org_id, id) ON DELETE CASCADE
);
CREATE INDEX update_rollouts_created_idx ON update_rollouts (org_id, created_at);
ALTER TABLE update_rollouts ENABLE ROW LEVEL SECURITY;
ALTER TABLE update_rollouts FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON update_rollouts
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON update_rollouts TO sermon_app;

-- state: 1 waiting, 2 updating, 3 rebooting, 4 health check, 5 done, 6 failed, 7 skipped.
CREATE TABLE update_rollout_nodes (
    org_id     uuid        NOT NULL,
    rollout_id uuid        NOT NULL,
    node_id    uuid        NOT NULL,
    position   integer     NOT NULL CHECK (position >= 0),
    hostname   text        NOT NULL DEFAULT '' CHECK (char_length(hostname) <= 253),
    state      smallint    NOT NULL CHECK (state BETWEEN 1 AND 7),
    reason     text        NOT NULL DEFAULT '' CHECK (octet_length(reason) <= 1024),
    update_run uuid,
    reboot_run uuid,
    since      timestamptz NOT NULL,
    deadline   timestamptz,
    PRIMARY KEY (org_id, rollout_id, node_id),
    FOREIGN KEY (org_id, rollout_id) REFERENCES update_rollouts (org_id, id) ON DELETE CASCADE,
    FOREIGN KEY (org_id, node_id) REFERENCES nodes (org_id, id) ON DELETE CASCADE
);
ALTER TABLE update_rollout_nodes ENABLE ROW LEVEL SECURITY;
ALTER TABLE update_rollout_nodes FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON update_rollout_nodes
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON update_rollout_nodes TO sermon_app;

-- Retention of the runs (a year, KONZEPT §4.5): deleted per organization by the orchestrator.
CREATE INDEX node_commands_created_idx ON node_commands (org_id, created_at);
GRANT DELETE ON node_commands TO sermon_app;

ALTER TABLE security_events DROP CONSTRAINT security_events_kind_check;
ALTER TABLE security_events ADD CONSTRAINT security_events_kind_check CHECK (kind IN (
    'ssh.login.failed', 'ssh.login.invalid_user', 'ssh.login.success', 'ssh.preauth.disconnect',
    'sudo.auth.failed', 'sudo.not_allowed', 'sudo.command', 'account.created', 'account.deleted',
    'system.reboot', 'system.oom_kill',
    'enrollment.token_used', 'node.locked', 'node.rotation_new_ip',
    'updates.run_failed', 'updates.rollout_stopped'));

-- +goose Down
DELETE FROM security_events WHERE kind IN ('updates.run_failed', 'updates.rollout_stopped');
ALTER TABLE security_events DROP CONSTRAINT security_events_kind_check;
ALTER TABLE security_events ADD CONSTRAINT security_events_kind_check CHECK (kind IN (
    'ssh.login.failed', 'ssh.login.invalid_user', 'ssh.login.success', 'ssh.preauth.disconnect',
    'sudo.auth.failed', 'sudo.not_allowed', 'sudo.command', 'account.created', 'account.deleted',
    'system.reboot', 'system.oom_kill',
    'enrollment.token_used', 'node.locked', 'node.rotation_new_ip'));
REVOKE DELETE ON node_commands FROM sermon_app;
DROP INDEX node_commands_created_idx;
DROP TABLE update_rollout_nodes;
DROP TABLE update_rollouts;
DROP TABLE update_leases;
ALTER TABLE nodes DROP COLUMN connected_at;
