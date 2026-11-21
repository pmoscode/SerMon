-- Silences and maintenance windows (KONZEPT §12.1, M2-15). Both are tenant tables with RLS
-- (ADR 0007).
--
-- A silence mutes the notifications of matching alerts (matchers as in notification_policies,
-- optionally single nodes) from starts_at to ends_at; ending it early moves ends_at. Ended
-- silences are kept 30 days for the record. A maintenance window mutes the reboot and offline
-- alerts of its groups (all groups if none) every week on its days, in its time zone.
--
-- alert_routing_state now also remembers what a silence held back (muted) and when the channels
-- first heard of an alert (escalation); the fallback without policies keeps states under the
-- zero policy ID, so the state no longer refers to notification_policies (the router drops the
-- states of policies that are gone).

-- +goose Up
ALTER TABLE alert_routing_state DROP CONSTRAINT alert_routing_state_org_id_policy_id_fkey;
ALTER TABLE alert_routing_state ALTER COLUMN last_sent_at DROP NOT NULL;
ALTER TABLE alert_routing_state ADD COLUMN first_sent_at timestamptz;
ALTER TABLE alert_routing_state ADD COLUMN muted boolean NOT NULL DEFAULT false;

CREATE TABLE alert_silences (
    org_id     uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    id         uuid        NOT NULL,
    matchers   jsonb       NOT NULL DEFAULT '{}'
        CHECK (jsonb_typeof(matchers) = 'object' AND octet_length(matchers::text) <= 16384),
    node_ids   uuid[]      NOT NULL DEFAULT '{}' CHECK (cardinality(node_ids) <= 50),
    starts_at  timestamptz NOT NULL,
    ends_at    timestamptz NOT NULL,
    comment    text        NOT NULL CHECK (char_length(comment) BETWEEN 1 AND 500),
    created_at timestamptz NOT NULL,
    created_by uuid,
    expired_by uuid,
    PRIMARY KEY (org_id, id),
    CHECK (ends_at >= starts_at)
);
CREATE INDEX alert_silences_ends_idx ON alert_silences (org_id, ends_at);
ALTER TABLE alert_silences ENABLE ROW LEVEL SECURITY;
ALTER TABLE alert_silences FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON alert_silences
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON alert_silences TO sermon_app;

CREATE TABLE maintenance_windows (
    org_id           uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    id               uuid        NOT NULL,
    name             text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    enabled          boolean     NOT NULL,
    group_ids        uuid[]      NOT NULL DEFAULT '{}' CHECK (cardinality(group_ids) <= 20),
    days             smallint[]  NOT NULL CHECK (cardinality(days) BETWEEN 1 AND 7 AND days <@ '{0,1,2,3,4,5,6}'),
    start_minute     integer     NOT NULL CHECK (start_minute BETWEEN 0 AND 1439),
    duration_minutes integer     NOT NULL CHECK (duration_minutes BETWEEN 15 AND 1440),
    time_zone        text        NOT NULL CHECK (char_length(time_zone) BETWEEN 1 AND 64),
    created_at       timestamptz NOT NULL,
    updated_at       timestamptz NOT NULL,
    created_by       uuid,
    PRIMARY KEY (org_id, id)
);
ALTER TABLE maintenance_windows ENABLE ROW LEVEL SECURITY;
ALTER TABLE maintenance_windows FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON maintenance_windows
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON maintenance_windows TO sermon_app;

-- +goose Down
DROP TABLE maintenance_windows;
DROP TABLE alert_silences;
DELETE FROM alert_routing_state WHERE policy_id NOT IN (SELECT id FROM notification_policies) OR last_sent_at IS NULL;
ALTER TABLE alert_routing_state DROP COLUMN muted;
ALTER TABLE alert_routing_state DROP COLUMN first_sent_at;
ALTER TABLE alert_routing_state ALTER COLUMN last_sent_at SET NOT NULL;
ALTER TABLE alert_routing_state ADD FOREIGN KEY (org_id, policy_id) REFERENCES notification_policies (org_id, id) ON DELETE CASCADE;
