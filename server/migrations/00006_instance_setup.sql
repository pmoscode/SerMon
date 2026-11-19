-- First installation (KONZEPT §16, IMPLEMENTIERUNGSPLAN M1-05): one-time setup token and superadmins.
-- Both tables are instance-level (no org_id, no RLS; ADR 0007).

-- +goose Up

-- Exactly one row. The setup token is stored only as SHA-256; claiming it completes the setup
-- once (UPDATE … WHERE completed_at IS NULL – concurrent claims have a single winner).
CREATE TABLE instance_setup (
    singleton        boolean     PRIMARY KEY DEFAULT true CHECK (singleton),
    token_hash       bytea       CHECK (token_hash IS NULL OR length(token_hash) = 32),
    token_expires_at timestamptz,
    completed_at     timestamptz,
    CHECK ((token_hash IS NULL) = (token_expires_at IS NULL)),
    CHECK (completed_at IS NULL OR token_hash IS NULL)
);
GRANT SELECT, INSERT, UPDATE ON instance_setup TO sermon_app;

-- Superadmins manage organizations; they see tenant data only through a membership (KONZEPT §7).
CREATE TABLE superadmins (
    user_id    uuid        PRIMARY KEY REFERENCES users (id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now()
);
GRANT SELECT, INSERT ON superadmins TO sermon_app;

-- +goose Down
DROP TABLE superadmins;
DROP TABLE instance_setup;
