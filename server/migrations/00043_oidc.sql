-- Login over an OpenID Connect provider (M3-20, ADR 0033). Instance-level like the credentials:
-- a user is found by issuer and subject, never by the e-mail alone.
--
-- oidc_flows holds a started login until the provider returns (10 minutes, once): state, nonce
-- and the PKCE verifier, bound to a cookie of the browser that started it (binding_hash), with
-- the intent – log in, link an identity to the signed-in user or renew the step-up.

-- +goose Up
CREATE TABLE user_identities (
    issuer        text        NOT NULL CHECK (octet_length(issuer) BETWEEN 1 AND 512),
    subject       text        NOT NULL CHECK (octet_length(subject) BETWEEN 1 AND 255),
    user_id       uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    email         text        CHECK (octet_length(email) <= 320),
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_login_at timestamptz,
    PRIMARY KEY (issuer, subject),
    UNIQUE (user_id, issuer)
);
GRANT SELECT, INSERT, UPDATE, DELETE ON user_identities TO sermon_app;

CREATE TABLE oidc_flows (
    state_hash   text        PRIMARY KEY CHECK (state_hash ~ '^[0-9a-f]{64}$'),
    binding_hash text        NOT NULL CHECK (binding_hash ~ '^[0-9a-f]{64}$'),
    nonce        text        NOT NULL CHECK (octet_length(nonce) BETWEEN 16 AND 128),
    verifier     text        NOT NULL CHECK (octet_length(verifier) BETWEEN 43 AND 128),
    intent       smallint    NOT NULL CHECK (intent BETWEEN 1 AND 3),
    user_id      uuid        REFERENCES users (id) ON DELETE CASCADE,
    session_id   text        REFERENCES sessions (id) ON DELETE CASCADE,
    next_path    text        NOT NULL CHECK (octet_length(next_path) BETWEEN 1 AND 512),
    created_at   timestamptz NOT NULL,
    expires_at   timestamptz NOT NULL,
    CHECK ((intent = 1) = (user_id IS NULL AND session_id IS NULL))
);
CREATE INDEX oidc_flows_expires_idx ON oidc_flows (expires_at);
GRANT SELECT, INSERT, DELETE ON oidc_flows TO sermon_app;

-- A login at the provider with several factors (amr, or trusted by the operator) satisfies an
-- organization's two-factor requirement for this session.
ALTER TABLE sessions ADD COLUMN external_mfa boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE sessions DROP COLUMN external_mfa;
DROP TABLE oidc_flows;
DROP TABLE user_identities;
