-- Superadmins come only from the setup token (M1-40): sermon_app may no longer insert into
-- superadmins or change instance_setup. A bug in the application (e.g. an SQL injection) could
-- otherwise add a superadmin directly, or reset the setup and issue itself a new token. Issuing
-- and consuming the token run in SECURITY DEFINER functions owned by sermon_definer; consuming
-- claims the token and adds the superadmin in one statement.

-- +goose Up
GRANT SELECT, INSERT, UPDATE ON instance_setup TO sermon_definer;
GRANT INSERT ON superadmins TO sermon_definer;
GRANT CREATE ON SCHEMA public TO sermon_definer;

-- Replaces a previous token; false once the setup is completed.
CREATE FUNCTION issue_setup_token(p_hash bytea, p_expires timestamptz) RETURNS boolean
    LANGUAGE sql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        WITH issued AS (
            INSERT INTO public.instance_setup (singleton, token_hash, token_expires_at)
            VALUES (true, p_hash, p_expires)
            ON CONFLICT (singleton) DO UPDATE
                SET token_hash = EXCLUDED.token_hash, token_expires_at = EXCLUDED.token_expires_at
                WHERE instance_setup.completed_at IS NULL
            RETURNING 1
        )
        SELECT EXISTS (SELECT 1 FROM issued)
    $$;

-- Claims a valid token once and makes p_user the first superadmin; false without a valid token
-- (concurrent claims have a single winner). The user must exist in the same transaction.
CREATE FUNCTION complete_instance_setup(p_hash bytea, p_user uuid, p_now timestamptz) RETURNS boolean
    LANGUAGE sql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
        WITH claimed AS (
            UPDATE public.instance_setup
            SET completed_at = p_now, token_hash = NULL, token_expires_at = NULL
            WHERE token_hash = p_hash AND token_expires_at > p_now AND completed_at IS NULL
            RETURNING 1
        ), added AS (
            INSERT INTO public.superadmins (user_id) SELECT p_user FROM claimed
            RETURNING 1
        )
        SELECT EXISTS (SELECT 1 FROM added)
    $$;

ALTER FUNCTION issue_setup_token(bytea, timestamptz) OWNER TO sermon_definer;
ALTER FUNCTION complete_instance_setup(bytea, uuid, timestamptz) OWNER TO sermon_definer;
REVOKE ALL ON FUNCTION issue_setup_token(bytea, timestamptz) FROM PUBLIC;
REVOKE ALL ON FUNCTION complete_instance_setup(bytea, uuid, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION issue_setup_token(bytea, timestamptz) TO sermon_app;
GRANT EXECUTE ON FUNCTION complete_instance_setup(bytea, uuid, timestamptz) TO sermon_app;
REVOKE CREATE ON SCHEMA public FROM sermon_definer;

REVOKE INSERT ON superadmins FROM sermon_app;
REVOKE INSERT, UPDATE ON instance_setup FROM sermon_app;

-- +goose Down
GRANT INSERT, UPDATE ON instance_setup TO sermon_app;
GRANT INSERT ON superadmins TO sermon_app;
DROP FUNCTION complete_instance_setup(bytea, uuid, timestamptz);
DROP FUNCTION issue_setup_token(bytea, timestamptz);
REVOKE INSERT ON superadmins FROM sermon_definer;
REVOKE SELECT, INSERT, UPDATE ON instance_setup FROM sermon_definer;
