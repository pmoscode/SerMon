-- Fields of alerts (M2-08): event rules pass on what the notification shows (user, IP, port of
-- the latest event; KONZEPT §12.2 notifyFields). The instance keeps those of its last change,
-- each history row those of its transition. The engine replaces control characters and bounds
-- each value to 256 bytes; the check is the backstop.

-- +goose Up
ALTER TABLE alert_instances ADD COLUMN fields jsonb NOT NULL DEFAULT '{}'
    CHECK (jsonb_typeof(fields) = 'object' AND octet_length(fields::text) <= 16384);
ALTER TABLE alert_history ADD COLUMN fields jsonb NOT NULL DEFAULT '{}'
    CHECK (jsonb_typeof(fields) = 'object' AND octet_length(fields::text) <= 16384);

-- +goose Down
ALTER TABLE alert_history DROP COLUMN fields;
ALTER TABLE alert_instances DROP COLUMN fields;
