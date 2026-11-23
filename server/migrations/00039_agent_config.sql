-- Configuration from the server to the agents (ADR 0030, M3-03): log settings per node group,
-- redaction rules and a version per organization, and what each agent reported back.

-- +goose Up
-- sermon_app already may update organizations of its tenant (RLS, migration 00001). Every change
-- of log settings or rules raises the version in the same transaction.
ALTER TABLE organizations
    ADD COLUMN agent_config_version bigint NOT NULL DEFAULT 1 CHECK (agent_config_version >= 1),
    ADD COLUMN redaction_rules      jsonb  NOT NULL DEFAULT '[]'
        CHECK (jsonb_typeof(redaction_rules) = 'array' AND octet_length(redaction_rules::text) <= 65536);

-- A group without a row has the defaults (logs on, no filter). The document is
-- logconfig.Settings as JSON, checked by the server.
CREATE TABLE log_settings (
    org_id     uuid        NOT NULL,
    group_id   uuid        NOT NULL,
    settings   jsonb       NOT NULL CHECK (jsonb_typeof(settings) = 'object' AND octet_length(settings::text) <= 65536),
    updated_at timestamptz NOT NULL DEFAULT now(),
    updated_by uuid,
    PRIMARY KEY (org_id, group_id),
    FOREIGN KEY (org_id, group_id) REFERENCES node_groups (org_id, id) ON DELETE CASCADE
);
ALTER TABLE log_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE log_settings FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON log_settings
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON log_settings TO sermon_app;

-- The answer of the agent (ConfigApplied): the version it was sent, why it kept its previous
-- configuration (chosen by the host: shown as text only) and the local veto.
ALTER TABLE nodes
    ADD COLUMN config_version     bigint      NOT NULL DEFAULT 0 CHECK (config_version >= 0),
    ADD COLUMN config_error       text        CHECK (octet_length(config_error) <= 1024),
    ADD COLUMN logs_vetoed        boolean     NOT NULL DEFAULT false,
    ADD COLUMN config_reported_at timestamptz;

-- +goose Down
ALTER TABLE nodes
    DROP COLUMN config_reported_at,
    DROP COLUMN logs_vetoed,
    DROP COLUMN config_error,
    DROP COLUMN config_version;
DROP TABLE log_settings;
ALTER TABLE organizations
    DROP COLUMN redaction_rules,
    DROP COLUMN agent_config_version;
