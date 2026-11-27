-- Users change their own e-mail address and display name (V1-17); the tenant policies of users
-- don't allow it (00016), so only these functions do, for the user the server names from the
-- session:
--   * account_set_display_name, account_set_email (false: the address belongs to another account);
--   * email_changes holds a pending change until the code from the mail to the new address is
--     entered: the code only as a hash, a few attempts, an hour;
--   * account_export reads the account's own row for the export of its data.

-- +goose Up
GRANT CREATE ON SCHEMA public TO sermon_definer;

CREATE TABLE email_changes (
    user_id    uuid        PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    new_email  text        NOT NULL CHECK (new_email = lower(new_email) AND char_length(new_email) BETWEEN 3 AND 320),
    code_hash  bytea       NOT NULL CHECK (length(code_hash) = 32),
    attempts   integer     NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 100),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);
CREATE INDEX email_changes_expires_idx ON email_changes (expires_at);
GRANT SELECT, INSERT, UPDATE, DELETE ON email_changes TO sermon_app;

GRANT UPDATE (email, display_name) ON users TO sermon_definer;

-- +goose StatementBegin
CREATE FUNCTION account_set_display_name(p_user uuid, p_name text) RETURNS boolean
    LANGUAGE sql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
    WITH changed AS (UPDATE public.users SET display_name = p_name WHERE id = p_user RETURNING 1)
    SELECT EXISTS (SELECT 1 FROM changed)
$$;
-- +goose StatementEnd
ALTER FUNCTION account_set_display_name(uuid, text) OWNER TO sermon_definer;
REVOKE ALL ON FUNCTION account_set_display_name(uuid, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION account_set_display_name(uuid, text) TO sermon_app;

-- +goose StatementBegin
CREATE FUNCTION account_set_email(p_user uuid, p_email text) RETURNS boolean
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
BEGIN
    UPDATE public.users SET email = p_email WHERE id = p_user;
    RETURN FOUND;
EXCEPTION WHEN unique_violation THEN
    RETURN false;
END
$$;
-- +goose StatementEnd
ALTER FUNCTION account_set_email(uuid, text) OWNER TO sermon_definer;
REVOKE ALL ON FUNCTION account_set_email(uuid, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION account_set_email(uuid, text) TO sermon_app;

-- +goose StatementBegin
CREATE FUNCTION account_export(p_user uuid) RETURNS TABLE (email text, display_name text, created_at timestamptz)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$ SELECT u.email, u.display_name, u.created_at FROM public.users u WHERE u.id = p_user $$;
-- +goose StatementEnd
ALTER FUNCTION account_export(uuid) OWNER TO sermon_definer;
REVOKE ALL ON FUNCTION account_export(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION account_export(uuid) TO sermon_app;

REVOKE CREATE ON SCHEMA public FROM sermon_definer;

-- +goose Down
DROP FUNCTION account_export(uuid);
DROP FUNCTION account_set_email(uuid, text);
DROP FUNCTION account_set_display_name(uuid, text);
REVOKE UPDATE (email, display_name) ON users FROM sermon_definer;
DROP TABLE email_changes;
