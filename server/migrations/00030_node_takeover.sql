-- Node takeover (M2-23, T5): an operator allows that the next enrollment with the node's
-- machine-id and a token of its group takes the node over (same ID and history, new key) until
-- this time. NULL: no takeover allowed.

-- +goose Up
ALTER TABLE nodes ADD COLUMN takeover_until timestamptz;

-- +goose Down
ALTER TABLE nodes DROP COLUMN takeover_until;
