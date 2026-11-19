-- Profile of the logged-in user without tenant (session view): e-mail and display name only.

-- +goose Up
GRANT CREATE ON SCHEMA public TO sermon_definer;
CREATE FUNCTION auth_user_profile(p_user uuid) RETURNS TABLE (email text, display_name text)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$ SELECT email, display_name FROM public.users WHERE id = p_user $$;
ALTER FUNCTION auth_user_profile(uuid) OWNER TO sermon_definer;
REVOKE CREATE ON SCHEMA public FROM sermon_definer;
REVOKE ALL ON FUNCTION auth_user_profile(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_user_profile(uuid) TO sermon_app;

-- +goose Down
DROP FUNCTION auth_user_profile(uuid);
