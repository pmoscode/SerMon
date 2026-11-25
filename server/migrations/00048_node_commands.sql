-- Commands of the server to the agents (M4-11, ADR 0015 addendum): refresh the update
-- information, install updates, reboot, replace the agent. The instance holding the node's
-- session delivers them; a NOTIFY on sermon_node_commands (payload: the node's ID, nothing else)
-- wakes it, a poll is the fallback. The run ID (id) makes a command idempotent on the agent:
-- after a reconnect a sent command without result goes out again.
--
-- state: 1 pending, 2 sent, 3 running, 4 done, 5 expired (a command still waiting after
-- expires_at; marked when the node's session looks, shown so by reads before). phase and the
-- result columns come from the agent's CommandResult; output is its redacted end, at most 256 KiB.

-- +goose Up
CREATE TABLE node_commands (
    org_id           uuid        NOT NULL,
    id               uuid        NOT NULL,
    node_id          uuid        NOT NULL,
    kind             smallint    NOT NULL CHECK (kind BETWEEN 1 AND 4),
    scope            smallint    CHECK (scope BETWEEN 1 AND 2),
    reason           text        NOT NULL DEFAULT '' CHECK (octet_length(reason) <= 200),
    version          text        NOT NULL DEFAULT '' CHECK (octet_length(version) <= 64),
    state            smallint    NOT NULL DEFAULT 1 CHECK (state BETWEEN 1 AND 5),
    created_at       timestamptz NOT NULL,
    expires_at       timestamptz NOT NULL CHECK (expires_at > created_at),
    created_by       uuid        REFERENCES users (id) ON DELETE SET NULL,
    sent_at          timestamptz,
    phase            smallint    CHECK (phase BETWEEN 1 AND 6),
    exit_code        integer,
    result_reason    text        NOT NULL DEFAULT '' CHECK (octet_length(result_reason) <= 1024),
    output           text        NOT NULL DEFAULT '' CHECK (octet_length(output) <= 262144),
    output_truncated boolean     NOT NULL DEFAULT false,
    started_at       timestamptz,
    ended_at         timestamptz,
    PRIMARY KEY (org_id, id),
    FOREIGN KEY (org_id, node_id) REFERENCES nodes (org_id, id) ON DELETE CASCADE,
    CHECK ((kind = 2) = (scope IS NOT NULL))
);
CREATE INDEX node_commands_waiting_idx ON node_commands (org_id, node_id, created_at) WHERE state IN (1, 2);
CREATE INDEX node_commands_node_idx ON node_commands (org_id, node_id, created_at DESC);
ALTER TABLE node_commands ENABLE ROW LEVEL SECURITY;
ALTER TABLE node_commands FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON node_commands
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT, UPDATE ON node_commands TO sermon_app;

-- +goose Down
DROP TABLE node_commands;
