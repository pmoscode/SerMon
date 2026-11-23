-- The switch "viewers may not see logs" of an organization (KONZEPT §7, T22, M3-12): logs and
-- security events contain personal data. Read with the principal on every request.

-- +goose Up
ALTER TABLE organizations ADD COLUMN viewers_see_logs boolean NOT NULL DEFAULT true;

-- +goose Down
ALTER TABLE organizations DROP COLUMN viewers_see_logs;
