-- Where the agent of a node reads security events (M2-31, ADR 0025), from the host facts of its
-- last Hello: the journal, the file of the SerMon rsyslog template, another log file, or nothing.
-- Empty: not reported (agents before M2-31, nodes that never connected). The nodes table already
-- has row-level security.

-- +goose Up
ALTER TABLE nodes ADD COLUMN security_source text NOT NULL DEFAULT ''
    CHECK (security_source IN ('', 'journal', 'file_verified', 'file_unverified', 'none'));

-- +goose Down
ALTER TABLE nodes DROP COLUMN security_source;
