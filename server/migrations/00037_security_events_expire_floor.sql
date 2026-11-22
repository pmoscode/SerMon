-- Retention of security events (M2-28): security_events_expire drops whole months before the time
-- it is given; the server passes its clock minus a year. A clock jumping ahead (or a bug passing
-- 'infinity') would have dropped every partition of every organization. Now the database's own
-- clock bounds it: nothing of the last 365 days goes, as alert_history's delete policy does.

-- +goose Up
GRANT CREATE ON SCHEMA public TO sermon_definer;
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION security_events_expire(p_before timestamptz) RETURNS integer
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
DECLARE
    partition record;
    dropped integer := 0;
BEGIN
    -- Never a month of the last year, whatever the caller's clock says (ADR 0018).
    p_before := least(p_before, now() - interval '365 days');
    PERFORM pg_advisory_xact_lock(hashtext('security_events_partitions'));
    FOR partition IN
        SELECT c.relname
        FROM pg_inherits i
        JOIN pg_class c ON c.oid = i.inhrelid
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE i.inhparent = 'public.security_events'::regclass AND n.nspname = 'public'
          AND c.relname ~ '^security_events_[0-9]{6}$'
    LOOP
        IF (to_timestamp(substring(partition.relname FROM 17), 'YYYYMM') AT TIME ZONE 'UTC')::timestamp
               + interval '1 month' <= (p_before AT TIME ZONE 'UTC') THEN
            EXECUTE format('DROP TABLE public.%I', partition.relname);
            dropped := dropped + 1;
        END IF;
    END LOOP;
    RETURN dropped;
END
$$;
-- +goose StatementEnd
ALTER FUNCTION security_events_expire(timestamptz) OWNER TO sermon_definer;
REVOKE CREATE ON SCHEMA public FROM sermon_definer;

-- +goose Down
GRANT CREATE ON SCHEMA public TO sermon_definer;
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION security_events_expire(p_before timestamptz) RETURNS integer
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $$
DECLARE
    partition record;
    dropped integer := 0;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtext('security_events_partitions'));
    FOR partition IN
        SELECT c.relname
        FROM pg_inherits i
        JOIN pg_class c ON c.oid = i.inhrelid
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE i.inhparent = 'public.security_events'::regclass AND n.nspname = 'public'
          AND c.relname ~ '^security_events_[0-9]{6}$'
    LOOP
        IF (to_timestamp(substring(partition.relname FROM 17), 'YYYYMM') AT TIME ZONE 'UTC')::timestamp
               + interval '1 month' <= (p_before AT TIME ZONE 'UTC') THEN
            EXECUTE format('DROP TABLE public.%I', partition.relname);
            dropped := dropped + 1;
        END IF;
    END LOOP;
    RETURN dropped;
END
$$;
-- +goose StatementEnd
ALTER FUNCTION security_events_expire(timestamptz) OWNER TO sermon_definer;
REVOKE CREATE ON SCHEMA public FROM sermon_definer;
