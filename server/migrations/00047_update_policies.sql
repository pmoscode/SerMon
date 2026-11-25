-- Update policies of the node groups (M4-10, KONZEPT §14.2): what a group does with its updates
-- on its own. A group without a row reports only. The window is a weekly schedule like a
-- maintenance window (days 0 = Sunday to 6, start and length in minutes, an IANA time zone);
-- the modes that install need one. Changes are audited by the application.

-- +goose Up
CREATE TABLE update_policies (
    org_id              uuid        NOT NULL,
    group_id            uuid        NOT NULL,
    mode                smallint    NOT NULL CHECK (mode BETWEEN 1 AND 3),
    window_days         smallint[]  CHECK (cardinality(window_days) BETWEEN 1 AND 7 AND 0 <= ALL (window_days) AND 6 >= ALL (window_days)),
    window_start_minute integer     CHECK (window_start_minute BETWEEN 0 AND 1439),
    window_minutes      integer     CHECK (window_minutes BETWEEN 15 AND 1440),
    window_time_zone    text        CHECK (char_length(window_time_zone) BETWEEN 1 AND 64),
    auto_reboot         boolean     NOT NULL,
    max_parallel        integer     NOT NULL CHECK (max_parallel BETWEEN 1 AND 50),
    stop_on_failure     boolean     NOT NULL,
    health_check_minutes integer    NOT NULL CHECK (health_check_minutes BETWEEN 5 AND 120),
    updated_at          timestamptz NOT NULL,
    updated_by          uuid        REFERENCES users (id) ON DELETE SET NULL,
    PRIMARY KEY (org_id, group_id),
    FOREIGN KEY (org_id, group_id) REFERENCES node_groups (org_id, id) ON DELETE CASCADE,
    -- A window is complete or absent; installing and rebooting need one.
    CHECK ((window_days IS NULL) = (window_start_minute IS NULL)
       AND (window_days IS NULL) = (window_minutes IS NULL)
       AND (window_days IS NULL) = (window_time_zone IS NULL)),
    CHECK (window_days IS NOT NULL OR (mode = 1 AND NOT auto_reboot))
);
ALTER TABLE update_policies ENABLE ROW LEVEL SECURITY;
ALTER TABLE update_policies FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON update_policies
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON update_policies TO sermon_app;

-- +goose Down
DROP TABLE update_policies;
