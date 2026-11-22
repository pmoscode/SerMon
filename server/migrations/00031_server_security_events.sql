-- Security events the server writes itself (M2-24, T3, T4): an enrollment token was used, a node
-- was locked, a node rotated its key from a new address. They live next to the agent's events, so
-- the same rules, retention and rights apply.

-- +goose Up
ALTER TABLE security_events DROP CONSTRAINT security_events_kind_check;
ALTER TABLE security_events ADD CONSTRAINT security_events_kind_check CHECK (kind IN (
    'ssh.login.failed', 'ssh.login.invalid_user', 'ssh.login.success', 'ssh.preauth.disconnect',
    'sudo.auth.failed', 'sudo.not_allowed', 'sudo.command', 'account.created', 'account.deleted',
    'system.reboot', 'system.oom_kill',
    'enrollment.token_used', 'node.locked', 'node.rotation_new_ip'));

-- +goose Down
DELETE FROM security_events WHERE kind IN ('enrollment.token_used', 'node.locked', 'node.rotation_new_ip');
ALTER TABLE security_events DROP CONSTRAINT security_events_kind_check;
ALTER TABLE security_events ADD CONSTRAINT security_events_kind_check CHECK (kind IN (
    'ssh.login.failed', 'ssh.login.invalid_user', 'ssh.login.success', 'ssh.preauth.disconnect',
    'sudo.auth.failed', 'sudo.not_allowed', 'sudo.command', 'account.created', 'account.deleted',
    'system.reboot', 'system.oom_kill'));
