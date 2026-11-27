-- Deletion of personal data (V1-17, KONZEPT §18.7):
--   * an account remembers since when it has no membership (orphaned_since): a trigger on
--     memberships sets and clears it; accounts_purge deletes those without membership after the
--     grace (never superadmins), account_delete one on request of a superadmin;
--   * expired invitations are deleted with their e-mail address (invitations_purge_expired);
--   * the audit logs drop entries older than the instance's retention from the start of the chain
--     (audit_log_prune, instance_audit_log_prune); the server appends an entry naming the last
--     dropped one, so the chain stays verifiable from there.
-- Everything runs as sermon_definer: the trigger also fires in cascades outside any tenant.

-- +goose Up
GRANT CREATE ON SCHEMA public TO sermon_definer;

-- The owner does not pass the row-level security (FORCE) – lifted for the backfill.
ALTER TABLE users NO FORCE ROW LEVEL SECURITY;
ALTER TABLE memberships NO FORCE ROW LEVEL SECURITY;
ALTER TABLE users ADD COLUMN orphaned_since timestamptz DEFAULT now();
UPDATE users u SET orphaned_since = CASE
    WHEN EXISTS (SELECT 1 FROM memberships m WHERE m.user_id = u.id) THEN NULL ELSE now() END;
ALTER TABLE memberships FORCE ROW LEVEL SECURITY;
ALTER TABLE users FORCE ROW LEVEL SECURITY;
CREATE INDEX users_orphaned_idx ON users (orphaned_since) WHERE orphaned_since IS NOT NULL;

GRANT UPDATE (orphaned_since) ON users TO sermon_definer;
CREATE POLICY definer_orphan ON users FOR UPDATE TO sermon_definer USING (true) WITH CHECK (true);

-- +goose StatementBegin
CREATE FUNCTION users_track_membership() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        UPDATE public.users SET orphaned_since = NULL WHERE id = NEW.user_id AND orphaned_since IS NOT NULL;
    ELSE
        UPDATE public.users SET orphaned_since = now()
        WHERE id = OLD.user_id AND orphaned_since IS NULL
          AND NOT EXISTS (SELECT 1 FROM public.memberships m WHERE m.user_id = OLD.user_id);
    END IF;
    RETURN NULL;
END
$$;
-- +goose StatementEnd
ALTER FUNCTION users_track_membership() OWNER TO sermon_definer;
REVOKE ALL ON FUNCTION users_track_membership() FROM PUBLIC;
CREATE TRIGGER users_track_membership AFTER INSERT OR DELETE ON memberships
    FOR EACH ROW EXECUTE FUNCTION users_track_membership();

-- Accounts without membership since before p_before, oldest first; superadmins stay. The cascade
-- takes sessions, password, second factors, passkeys and identities; audit entries keep the ID.
-- +goose StatementBegin
CREATE FUNCTION accounts_purge(p_before timestamptz, p_limit integer) RETURNS SETOF uuid
    LANGUAGE sql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
    DELETE FROM public.users u
    WHERE u.id IN (
        SELECT c.id FROM public.users c
        WHERE c.orphaned_since < p_before
          AND NOT EXISTS (SELECT 1 FROM public.memberships m WHERE m.user_id = c.id)
          AND NOT EXISTS (SELECT 1 FROM public.superadmins s WHERE s.user_id = c.id)
        ORDER BY c.orphaned_since, c.id
        LIMIT p_limit
        FOR UPDATE)
    RETURNING u.id
$$;
-- +goose StatementEnd
ALTER FUNCTION accounts_purge(timestamptz, integer) OWNER TO sermon_definer;
REVOKE ALL ON FUNCTION accounts_purge(timestamptz, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION accounts_purge(timestamptz, integer) TO sermon_app;

-- Deletes an account on request (superadmin, step-up in the server): every organization it is a
-- member of gets an entry in its audit log by p_actor first. Refused (P0001) for superadmins and
-- for the last admin of all groups of an organization. Returns the number of memberships.
GRANT INSERT ON audit_log TO sermon_definer;
-- +goose StatementBegin
CREATE FUNCTION account_delete(p_user uuid, p_actor uuid) RETURNS integer
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
DECLARE
    v_org   uuid;
    v_count integer := 0;
BEGIN
    PERFORM 1 FROM public.users WHERE id = p_user FOR UPDATE;
    IF NOT FOUND THEN
        RETURN -1;
    END IF;
    IF EXISTS (SELECT 1 FROM public.superadmins WHERE user_id = p_user) THEN
        RAISE EXCEPTION 'account % is a superadmin', p_user USING ERRCODE = 'P0001', HINT = 'superadmin';
    END IF;
    FOR v_org IN SELECT m.org_id FROM public.memberships m WHERE m.user_id = p_user ORDER BY m.org_id LOOP
        IF EXISTS (SELECT 1 FROM public.role_bindings b
                   WHERE b.org_id = v_org AND b.user_id = p_user AND b.group_id IS NULL AND b.role = 'admin')
           AND NOT EXISTS (SELECT 1 FROM public.role_bindings b
                           WHERE b.org_id = v_org AND b.user_id <> p_user AND b.group_id IS NULL AND b.role = 'admin') THEN
            RAISE EXCEPTION 'account % is the last admin of organization %', p_user, v_org USING ERRCODE = 'P0001', HINT = 'last_admin';
        END IF;
        PERFORM set_config('sermon.org_id', v_org::text, true);
        INSERT INTO public.audit_log (org_id, actor_id, action, target, details)
        VALUES (v_org, p_actor, 'member.account_deleted', 'user:' || p_user::text, '{}');
        v_count := v_count + 1;
    END LOOP;
    PERFORM set_config('sermon.org_id', '', true);
    DELETE FROM public.users WHERE id = p_user;
    RETURN v_count;
END
$$;
-- +goose StatementEnd
ALTER FUNCTION account_delete(uuid, uuid) OWNER TO sermon_definer;
REVOKE ALL ON FUNCTION account_delete(uuid, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION account_delete(uuid, uuid) TO sermon_app;

-- Expired invitations go with their e-mail address.
GRANT DELETE ON invitations TO sermon_definer;
CREATE POLICY definer_purge ON invitations FOR DELETE TO sermon_definer USING (true);
-- +goose StatementBegin
CREATE FUNCTION invitations_purge_expired(p_before timestamptz) RETURNS bigint
    LANGUAGE sql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
    WITH gone AS (DELETE FROM public.invitations WHERE expires_at < p_before RETURNING 1)
    SELECT count(*) FROM gone
$$;
-- +goose StatementEnd
ALTER FUNCTION invitations_purge_expired(timestamptz) OWNER TO sermon_definer;
REVOKE ALL ON FUNCTION invitations_purge_expired(timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION invitations_purge_expired(timestamptz) TO sermon_app;

-- Audit retention: drops the entries before p_before from the start of the chain and returns the
-- last dropped one (nothing if none is that old). The newest entry always stays: the chain goes on
-- from it (a new entry after an empty log would start over at 1). Only inside the tenant.
-- +goose StatementBegin
CREATE FUNCTION audit_log_prune(p_org uuid, p_before timestamptz) RETURNS TABLE (through_seq bigint, last_hash bytea, deleted bigint)
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
DECLARE
    v_seq  bigint;
    v_hash bytea;
BEGIN
    IF current_setting('sermon.org_id', true) IS DISTINCT FROM p_org::text THEN
        RAISE EXCEPTION 'audit_log_prune outside the tenant of organization %', p_org USING ERRCODE = '42501';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended('audit:' || p_org::text, 0));
    SELECT a.seq, a.hash INTO v_seq, v_hash FROM public.audit_log a
    WHERE a.org_id = p_org AND a.at < p_before
      AND a.seq < (SELECT max(n.seq) FROM public.audit_log n WHERE n.org_id = p_org)
    ORDER BY a.seq DESC LIMIT 1;
    IF v_seq IS NULL THEN
        RETURN;
    END IF;
    DELETE FROM public.audit_log a WHERE a.org_id = p_org AND a.seq <= v_seq;
    GET DIAGNOSTICS deleted = ROW_COUNT;
    through_seq := v_seq;
    last_hash := v_hash;
    RETURN NEXT;
END
$$;
-- +goose StatementEnd
ALTER FUNCTION audit_log_prune(uuid, timestamptz) OWNER TO sermon_definer;
REVOKE ALL ON FUNCTION audit_log_prune(uuid, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION audit_log_prune(uuid, timestamptz) TO sermon_app;

GRANT SELECT, DELETE ON instance_audit_log TO sermon_definer;
-- +goose StatementBegin
CREATE FUNCTION instance_audit_log_prune(p_before timestamptz) RETURNS TABLE (through_seq bigint, last_hash bytea, deleted bigint)
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
DECLARE
    v_seq  bigint;
    v_hash bytea;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended('audit:instance', 0));
    SELECT a.seq, a.hash INTO v_seq, v_hash FROM public.instance_audit_log a
    WHERE a.at < p_before AND a.seq < (SELECT max(n.seq) FROM public.instance_audit_log n)
    ORDER BY a.seq DESC LIMIT 1;
    IF v_seq IS NULL THEN
        RETURN;
    END IF;
    DELETE FROM public.instance_audit_log a WHERE a.seq <= v_seq;
    GET DIAGNOSTICS deleted = ROW_COUNT;
    through_seq := v_seq;
    last_hash := v_hash;
    RETURN NEXT;
END
$$;
-- +goose StatementEnd
ALTER FUNCTION instance_audit_log_prune(timestamptz) OWNER TO sermon_definer;
REVOKE ALL ON FUNCTION instance_audit_log_prune(timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION instance_audit_log_prune(timestamptz) TO sermon_app;

REVOKE CREATE ON SCHEMA public FROM sermon_definer;

-- +goose Down
DROP FUNCTION instance_audit_log_prune(timestamptz);
REVOKE SELECT, DELETE ON instance_audit_log FROM sermon_definer;
DROP FUNCTION audit_log_prune(uuid, timestamptz);
DROP FUNCTION invitations_purge_expired(timestamptz);
DROP POLICY definer_purge ON invitations;
REVOKE DELETE ON invitations FROM sermon_definer;
DROP FUNCTION account_delete(uuid, uuid);
REVOKE INSERT ON audit_log FROM sermon_definer;
DROP FUNCTION accounts_purge(timestamptz, integer);
DROP TRIGGER users_track_membership ON memberships;
DROP FUNCTION users_track_membership();
DROP POLICY definer_orphan ON users;
REVOKE UPDATE (orphaned_since) ON users FROM sermon_definer;
DROP INDEX users_orphaned_idx;
ALTER TABLE users DROP COLUMN orphaned_since;
