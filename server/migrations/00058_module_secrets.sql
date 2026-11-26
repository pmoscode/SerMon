-- The credentials of the modules (M5-05, ADR 0039): sealed with secrets.Box, bound to
-- organization, group, module and field (associated data), so a ciphertext copied to another
-- record or organization doesn't open. Only the agents of the group get the values, over their
-- mTLS stream; the web API says only which fields have one.

-- +goose Up
CREATE TABLE module_secrets (
    org_id     uuid        NOT NULL,
    group_id   uuid        NOT NULL,
    module_id  text        NOT NULL,
    field      text        NOT NULL CHECK (field ~ '^[a-z][a-z0-9_]{0,63}$'),
    sealed     bytea       NOT NULL CHECK (octet_length(sealed) BETWEEN 1 AND 8192),
    updated_at timestamptz NOT NULL DEFAULT now(),
    updated_by uuid,
    PRIMARY KEY (org_id, group_id, module_id, field),
    -- Removing a module's setting removes its credentials.
    FOREIGN KEY (org_id, group_id, module_id) REFERENCES module_settings (org_id, group_id, module_id) ON DELETE CASCADE
);
ALTER TABLE module_secrets ENABLE ROW LEVEL SECURITY;
ALTER TABLE module_secrets FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON module_secrets
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON module_secrets TO sermon_app;

-- +goose Down
DROP TABLE module_secrets;
