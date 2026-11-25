-- Derived series of the updates (M4-16): since when a node needs a reboot, as SerMon saw it first.
-- Set when a report needs a reboot and the stored one didn't, kept while it does, cleared after.

-- +goose Up
ALTER TABLE node_update_state ADD COLUMN reboot_required_since timestamptz;
UPDATE node_update_state SET reboot_required_since = collected_at WHERE reboot_required;
ALTER TABLE node_update_state ADD CONSTRAINT node_update_state_reboot_since_check
    CHECK ((reboot_required_since IS NOT NULL) = reboot_required);

-- +goose Down
ALTER TABLE node_update_state DROP CONSTRAINT node_update_state_reboot_since_check;
ALTER TABLE node_update_state DROP COLUMN reboot_required_since;
