-- Alert rule overrides (KONZEPT §12.2, M2-04): per organization a default rule switched off or
-- replaced by its own document, or a rule of its own. The shipped defaults are embedded in the
-- server and never stored; the document is the AlertRule as JSON, checked by the server.

-- +goose Up
CREATE TABLE alert_rule_overrides (
    org_id     uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    rule_id    text        NOT NULL CHECK (rule_id ~ '^[a-z][a-z0-9_]{0,31}(\.[a-z][a-z0-9_]{0,31}){2,3}$'),
    disabled   boolean     NOT NULL DEFAULT false,
    document   jsonb       CHECK (octet_length(document::text) <= 65536),
    updated_at timestamptz NOT NULL DEFAULT now(),
    updated_by uuid,
    PRIMARY KEY (org_id, rule_id),
    -- A row changes something.
    CHECK (disabled OR document IS NOT NULL)
);
ALTER TABLE alert_rule_overrides ENABLE ROW LEVEL SECURITY;
ALTER TABLE alert_rule_overrides FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON alert_rule_overrides
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON alert_rule_overrides TO sermon_app;

-- +goose Down
DROP TABLE alert_rule_overrides;
