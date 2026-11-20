-- Two-factor authentication (KONZEPT §7, M1-03): TOTP secret (sealed with the master key, bound to
-- the user), recovery codes as hashes, sessions waiting for the second factor and the per-org
-- requirement.

-- +goose Up
CREATE TABLE user_totp (
    user_id       uuid        PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    secret_sealed bytea       NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    confirmed_at  timestamptz,
    -- Highest TOTP time step used; every step works once (replay protection).
    last_step     bigint      NOT NULL DEFAULT 0
);
GRANT SELECT, INSERT, UPDATE, DELETE ON user_totp TO sermon_app;

CREATE TABLE user_recovery_codes (
    user_id   uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    code_hash bytea       NOT NULL CHECK (octet_length(code_hash) = 32),
    used_at   timestamptz,
    PRIMARY KEY (user_id, code_hash)
);
GRANT SELECT, INSERT, UPDATE, DELETE ON user_recovery_codes TO sermon_app;

ALTER TABLE sessions ADD COLUMN second_factor_pending boolean NOT NULL DEFAULT false;

-- sermon_app already may update organizations of its tenant (RLS, migration 00001).
ALTER TABLE organizations ADD COLUMN require_second_factor boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE organizations DROP COLUMN require_second_factor;
ALTER TABLE sessions DROP COLUMN second_factor_pending;
DROP TABLE user_recovery_codes;
DROP TABLE user_totp;
