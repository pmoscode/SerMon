-- Custom dashboards (KONZEPT §16, M5-23, ADR 0040). A tenant table with RLS (ADR 0007): a
-- dashboard belongs to its organization and an owner; private ones are seen by the owner only,
-- shared ones by every member (the queries filter that, RLS keeps the organizations apart). The
-- document is the server's JSON of the model (domain/dashboard), checked before it is stored;
-- revision counts the changes against lost updates.

-- +goose Up
CREATE TABLE dashboards (
    org_id     uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    id         uuid        NOT NULL,
    owner_id   uuid        NOT NULL,
    title      text        NOT NULL CHECK (char_length(title) BETWEEN 1 AND 200),
    shared     boolean     NOT NULL DEFAULT false,
    revision   bigint      NOT NULL DEFAULT 1 CHECK (revision >= 1),
    spec       jsonb       NOT NULL CHECK (jsonb_typeof(spec) = 'object' AND octet_length(spec::text) <= 65536),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (org_id, id)
);
CREATE INDEX dashboards_owner_idx ON dashboards (org_id, owner_id);
ALTER TABLE dashboards ENABLE ROW LEVEL SECURITY;
ALTER TABLE dashboards FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON dashboards
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON dashboards TO sermon_app;

-- +goose Down
DROP TABLE dashboards;
