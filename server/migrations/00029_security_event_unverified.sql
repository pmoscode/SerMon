-- Security events from classic log files (M2-20, ADR 0025): on hosts without journald anyone could
-- have written the line, so the agent marks such events and the UI and the notifications say so.
-- A constant default needs no rewrite of the partitions.

-- +goose Up
ALTER TABLE security_events ADD COLUMN unverified boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE security_events DROP COLUMN unverified;
