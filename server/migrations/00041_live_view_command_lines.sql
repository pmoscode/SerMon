-- The switch "hide command lines" of an organization's live view (KONZEPT §9, M3-14): command
-- lines may contain secrets. Agent and server leave them out.

-- +goose Up
ALTER TABLE organizations ADD COLUMN live_hide_cmdline boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE organizations DROP COLUMN live_hide_cmdline;
