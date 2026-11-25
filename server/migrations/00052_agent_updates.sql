-- Self-updates of the agent from the server (M4-19, ADR 0036): how a node's agent was installed
-- (its Hello: 1 install.sh, 2 a package, 0 unknown), the release channel of a group (1 stable, 2
-- beta) and whether its policy updates the agent on its own in the window (1 manual, 2 auto). A
-- rollout node can update its agent first (state 8, agent_run). A failed self-update is a server
-- event (agent.self_update_failed).

-- +goose Up
ALTER TABLE nodes ADD COLUMN install_method smallint NOT NULL DEFAULT 0 CHECK (install_method BETWEEN 0 AND 2);
ALTER TABLE update_policies
    ADD COLUMN agent_update  smallint NOT NULL DEFAULT 1 CHECK (agent_update BETWEEN 1 AND 2),
    ADD COLUMN agent_channel smallint NOT NULL DEFAULT 1 CHECK (agent_channel BETWEEN 1 AND 2),
    ADD CONSTRAINT update_policies_agent_window_check CHECK (window_days IS NOT NULL OR agent_update = 1);
ALTER TABLE update_rollout_nodes ADD COLUMN agent_run uuid;
ALTER TABLE update_rollout_nodes DROP CONSTRAINT update_rollout_nodes_state_check;
ALTER TABLE update_rollout_nodes ADD CONSTRAINT update_rollout_nodes_state_check CHECK (state BETWEEN 1 AND 8);
ALTER TABLE security_events DROP CONSTRAINT security_events_kind_check;
ALTER TABLE security_events ADD CONSTRAINT security_events_kind_check CHECK (kind IN (
    'ssh.login.failed', 'ssh.login.invalid_user', 'ssh.login.success', 'ssh.preauth.disconnect',
    'sudo.auth.failed', 'sudo.not_allowed', 'sudo.command', 'account.created', 'account.deleted',
    'system.reboot', 'system.oom_kill',
    'enrollment.token_used', 'node.locked', 'node.rotation_new_ip',
    'updates.run_failed', 'updates.rollout_stopped', 'agent.self_update_failed'));

-- +goose Down
DELETE FROM security_events WHERE kind = 'agent.self_update_failed';
ALTER TABLE security_events DROP CONSTRAINT security_events_kind_check;
ALTER TABLE security_events ADD CONSTRAINT security_events_kind_check CHECK (kind IN (
    'ssh.login.failed', 'ssh.login.invalid_user', 'ssh.login.success', 'ssh.preauth.disconnect',
    'sudo.auth.failed', 'sudo.not_allowed', 'sudo.command', 'account.created', 'account.deleted',
    'system.reboot', 'system.oom_kill',
    'enrollment.token_used', 'node.locked', 'node.rotation_new_ip',
    'updates.run_failed', 'updates.rollout_stopped'));
DELETE FROM update_rollout_nodes WHERE state = 8;
ALTER TABLE update_rollout_nodes DROP CONSTRAINT update_rollout_nodes_state_check;
ALTER TABLE update_rollout_nodes ADD CONSTRAINT update_rollout_nodes_state_check CHECK (state BETWEEN 1 AND 7);
ALTER TABLE update_rollout_nodes DROP COLUMN agent_run;
ALTER TABLE update_policies DROP CONSTRAINT update_policies_agent_window_check,
    DROP COLUMN agent_channel, DROP COLUMN agent_update;
ALTER TABLE nodes DROP COLUMN install_method;
