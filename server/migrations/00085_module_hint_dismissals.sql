-- Dismissed suggestions of modules (V1-25): an admin hides the suggestions of a module for the
-- nodes of a group, for every member, until shown again. Who did it is in the audit log.

-- +goose Up
CREATE TABLE module_hint_dismissals (
    org_id       uuid        NOT NULL,
    group_id     uuid        NOT NULL,
    module_id    text        NOT NULL CHECK (module_id ~ '^[a-z][a-z0-9_]{0,31}$'),
    dismissed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, group_id, module_id),
    FOREIGN KEY (org_id, group_id) REFERENCES node_groups (org_id, id) ON DELETE CASCADE
);
ALTER TABLE module_hint_dismissals ENABLE ROW LEVEL SECURITY;
ALTER TABLE module_hint_dismissals FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON module_hint_dismissals
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT, DELETE ON module_hint_dismissals TO sermon_app;

-- +goose Down
DROP TABLE module_hint_dismissals;
