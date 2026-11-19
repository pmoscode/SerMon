-- Local authentication (KONZEPT §7, ADR 0007): credentials, sessions, tenant-less checks.

-- +goose Up

-- Tenant-less checks (login lookup, certificate revocation) run as SECURITY DEFINER functions
-- owned by sermon_definer: no login, no BYPASSRLS, only SELECT on the tables it needs and
-- permissive RLS policies for exactly this role. This works with FORCE ROW LEVEL SECURITY and a
-- non-superuser schema owner; sermon_app is not a member and stays fully tenant-scoped.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'sermon_definer') THEN
        CREATE ROLE sermon_definer NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
    END IF;
END
$$;
-- +goose StatementEnd
-- The migration role must be able to hand functions over to sermon_definer. A role that created
-- sermon_definer can grant it to itself; if the role was created by someone else (e.g. a DBA on a
-- managed PostgreSQL), that person must run the GRANT once.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT pg_has_role(current_user, 'sermon_definer', 'MEMBER') THEN
        BEGIN
            EXECUTE format('GRANT sermon_definer TO %I', current_user);
        EXCEPTION WHEN insufficient_privilege THEN
            RAISE EXCEPTION 'migration role % is not a member of sermon_definer', current_user
                USING HINT = format('Run once as a superuser: GRANT sermon_definer TO %I;', current_user);
        END;
    END IF;
END
$$;
-- +goose StatementEnd
-- A new function owner needs CREATE on the schema at the moment ownership changes; it is revoked
-- again right after (sermon_definer must not be able to create objects).
GRANT CREATE ON SCHEMA public TO sermon_definer;

GRANT SELECT ON users, memberships, organizations, agent_certificates TO sermon_definer;
CREATE POLICY definer_read ON users FOR SELECT TO sermon_definer USING (true);
CREATE POLICY definer_read ON memberships FOR SELECT TO sermon_definer USING (true);
CREATE POLICY definer_read ON organizations FOR SELECT TO sermon_definer USING (true);
CREATE POLICY definer_read ON agent_certificates FOR SELECT TO sermon_definer USING (true);

-- The revocation check (00002) moves to sermon_definer; agent_certificates gets FORCE like all
-- other tenant tables (the owner-bypass exception from M1-07 is no longer needed).
ALTER FUNCTION agent_certificate_usable(text, timestamptz) OWNER TO sermon_definer;
ALTER TABLE agent_certificates FORCE ROW LEVEL SECURITY;

-- Credentials of global user identities (instance-level, no tenant). Password hashes are Argon2id.
CREATE TABLE user_credentials (
    user_id             uuid        PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    password_hash       text        NOT NULL CHECK (password_hash LIKE '$argon2id$%'),
    password_changed_at timestamptz NOT NULL DEFAULT now(),
    failed_attempts     integer     NOT NULL DEFAULT 0 CHECK (failed_attempts >= 0),
    locked_until        timestamptz
);
GRANT SELECT, INSERT, UPDATE ON user_credentials TO sermon_app;

-- Server-side sessions; id is the SHA-256 (hex) of the cookie token, never the token itself.
CREATE TABLE sessions (
    id           text        PRIMARY KEY CHECK (id ~ '^[0-9a-f]{64}$'),
    user_id      uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    org_id       uuid        REFERENCES organizations (id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    step_up_at   timestamptz NOT NULL
);
CREATE INDEX sessions_user_idx ON sessions (user_id);
GRANT SELECT, INSERT, UPDATE, DELETE ON sessions TO sermon_app;

-- Login without tenant: e-mail → user ID only (case-insensitive, unique index users_email_key).
CREATE FUNCTION auth_find_user(p_email text) RETURNS uuid
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$ SELECT id FROM public.users WHERE lower(email) = lower(p_email) $$;
ALTER FUNCTION auth_find_user(text) OWNER TO sermon_definer;
REVOKE ALL ON FUNCTION auth_find_user(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_find_user(text) TO sermon_app;

-- Organizations of a user (login and organization switcher): only ID and name.
CREATE FUNCTION auth_user_organizations(p_user uuid) RETURNS TABLE (org_id uuid, name text)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        SELECT o.id, o.name
        FROM public.memberships m JOIN public.organizations o ON o.id = m.org_id
        WHERE m.user_id = p_user
        ORDER BY o.name
    $$;
ALTER FUNCTION auth_user_organizations(uuid) OWNER TO sermon_definer;
REVOKE ALL ON FUNCTION auth_user_organizations(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_user_organizations(uuid) TO sermon_app;

REVOKE CREATE ON SCHEMA public FROM sermon_definer;

-- +goose Down
DROP FUNCTION auth_user_organizations(uuid);
DROP FUNCTION auth_find_user(text);
DROP TABLE sessions;
DROP TABLE user_credentials;
ALTER TABLE agent_certificates NO FORCE ROW LEVEL SECURITY;
ALTER FUNCTION agent_certificate_usable(text, timestamptz) OWNER TO CURRENT_USER;
DROP POLICY definer_read ON agent_certificates;
DROP POLICY definer_read ON organizations;
DROP POLICY definer_read ON memberships;
DROP POLICY definer_read ON users;
REVOKE SELECT ON users, memberships, organizations, agent_certificates FROM sermon_definer;
