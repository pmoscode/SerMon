-- The agent's modules in the server (M5-06, ADR 0039): their settings per node group, what each
-- agent has built in and reported (health, discovery hints).

-- +goose Up
-- A module without a row is off. config is the module's configuration as checked JSON without
-- credentials (module.Schema); every change raises organizations.agent_config_version in the
-- same transaction (ADR 0030).
CREATE TABLE module_settings (
    org_id           uuid        NOT NULL,
    group_id         uuid        NOT NULL,
    module_id        text        NOT NULL CHECK (module_id ~ '^[a-z][a-z0-9_]{0,31}$'),
    enabled          boolean     NOT NULL,
    interval_seconds integer     CHECK (interval_seconds BETWEEN 10 AND 3600),
    config           jsonb       NOT NULL DEFAULT '{}'
        CHECK (jsonb_typeof(config) = 'object' AND octet_length(config::text) <= 16384),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    updated_by       uuid,
    PRIMARY KEY (org_id, group_id, module_id),
    FOREIGN KEY (org_id, group_id) REFERENCES node_groups (org_id, id) ON DELETE CASCADE
);
ALTER TABLE module_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE module_settings FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON module_settings
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON module_settings TO sermon_app;

-- What the agent has built in (Hello.modules) and whether agent.toml forbids modules.
ALTER TABLE nodes
    ADD COLUMN modules        text[]  NOT NULL DEFAULT '{}' CHECK (cardinality(modules) <= 64),
    ADD COLUMN modules_vetoed boolean NOT NULL DEFAULT false;

-- The newest health report of a node's agent, one row per enabled module (the report replaces
-- all of them). The reason comes from the host: shown as text only.
CREATE TABLE module_health (
    org_id       uuid        NOT NULL,
    node_id      uuid        NOT NULL,
    module_id    text        NOT NULL CHECK (module_id ~ '^[a-z][a-z0-9_]{0,31}$'),
    state        smallint    NOT NULL CHECK (state BETWEEN 1 AND 7),
    reason       text        NOT NULL DEFAULT '' CHECK (octet_length(reason) <= 256),
    last_success timestamptz,
    collected_at timestamptz NOT NULL,
    received_at  timestamptz NOT NULL,
    PRIMARY KEY (org_id, node_id, module_id),
    FOREIGN KEY (org_id, node_id) REFERENCES nodes (org_id, id) ON DELETE CASCADE
);
ALTER TABLE module_health ENABLE ROW LEVEL SECURITY;
ALTER TABLE module_health FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON module_health
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON module_health TO sermon_app;

-- The newest discovery hints of a node's agent: the module, what the agent saw and a
-- configuration to start the form with (checked against the module's schema).
CREATE TABLE module_hints (
    org_id       uuid        NOT NULL,
    node_id      uuid        NOT NULL,
    module_id    text        NOT NULL CHECK (module_id ~ '^[a-z][a-z0-9_]{0,31}$'),
    reasons      text[]      NOT NULL CHECK (cardinality(reasons) BETWEEN 1 AND 8),
    suggested    jsonb       CHECK (jsonb_typeof(suggested) = 'object' AND octet_length(suggested::text) <= 1024),
    collected_at timestamptz NOT NULL,
    received_at  timestamptz NOT NULL,
    PRIMARY KEY (org_id, node_id, module_id),
    FOREIGN KEY (org_id, node_id) REFERENCES nodes (org_id, id) ON DELETE CASCADE
);
ALTER TABLE module_hints ENABLE ROW LEVEL SECURITY;
ALTER TABLE module_hints FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON module_hints
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON module_hints TO sermon_app;

-- +goose Down
DROP TABLE module_hints;
DROP TABLE module_health;
ALTER TABLE nodes
    DROP COLUMN modules_vetoed,
    DROP COLUMN modules;
DROP TABLE module_settings;
