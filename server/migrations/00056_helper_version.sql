-- The root helper's version from a node's update report (M4-31, ADR 0036): empty without helper.
-- The UI shows a helper older than its agent, which needs install.sh once more.

-- +goose Up
ALTER TABLE node_update_state
    ADD COLUMN helper_version text NOT NULL DEFAULT '' CHECK (octet_length(helper_version) <= 64);

-- +goose Down
ALTER TABLE node_update_state DROP COLUMN helper_version;
