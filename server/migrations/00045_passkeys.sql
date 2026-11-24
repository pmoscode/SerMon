-- Passkeys (M3-23, ADR 0034). Instance-level like the credentials: a user's passkeys work in
-- every organization. credential holds the record of go-webauthn (public key, counter, flags)
-- as JSON; nothing in it is secret.
--
-- webauthn_ceremonies holds a started registration or assertion for 5 minutes, once: bound to
-- the session that began it, or for the sign-in without a password to a cookie of the browser.

-- +goose Up
CREATE TABLE webauthn_credentials (
    id           bytea       PRIMARY KEY CHECK (octet_length(id) BETWEEN 16 AND 1023),
    user_id      uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name         text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 64),
    credential   jsonb       NOT NULL CHECK (octet_length(credential::text) <= 16384),
    created_at   timestamptz NOT NULL,
    last_used_at timestamptz
);
CREATE INDEX webauthn_credentials_user_idx ON webauthn_credentials (user_id);
GRANT SELECT, INSERT, UPDATE, DELETE ON webauthn_credentials TO sermon_app;

CREATE TABLE webauthn_ceremonies (
    id_hash      text        PRIMARY KEY CHECK (id_hash ~ '^[0-9a-f]{64}$'),
    binding_hash text        CHECK (binding_hash ~ '^[0-9a-f]{64}$'),
    purpose      smallint    NOT NULL CHECK (purpose BETWEEN 1 AND 4),
    user_id      uuid        REFERENCES users (id) ON DELETE CASCADE,
    session_id   text        REFERENCES sessions (id) ON DELETE CASCADE,
    name         text        NOT NULL DEFAULT '' CHECK (char_length(name) <= 64),
    data         jsonb       NOT NULL CHECK (octet_length(data::text) <= 65536),
    created_at   timestamptz NOT NULL,
    expires_at   timestamptz NOT NULL,
    -- The sign-in has a browser and no user; every other purpose a user and its session.
    CHECK ((purpose = 2) = (binding_hash IS NOT NULL AND user_id IS NULL AND session_id IS NULL)),
    CHECK (purpose = 2 OR (user_id IS NOT NULL AND session_id IS NOT NULL))
);
CREATE INDEX webauthn_ceremonies_expires_idx ON webauthn_ceremonies (expires_at);
GRANT SELECT, INSERT, DELETE ON webauthn_ceremonies TO sermon_app;

-- +goose Down
DROP TABLE webauthn_ceremonies;
DROP TABLE webauthn_credentials;
