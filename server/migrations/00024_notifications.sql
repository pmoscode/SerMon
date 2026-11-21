-- Notification channels and the delivery log (KONZEPT §12.4, M2-10). Both are tenant tables with
-- RLS (ADR 0007). Credentials of a channel are sealed with secrets.Box (associated data
-- "notification_channel:<org>:<id>"); only their names are stored in clear.
--
-- notification_deliveries is the queue and the log at once: one row per notification and
-- channel with its attempts. Server instances claim due rows (FOR UPDATE SKIP LOCKED plus
-- claimed_until), so two instances never send the same row at once; an expired claim is taken
-- over. Finished rows are kept for 30 days (the server deletes older ones).

-- +goose Up
CREATE TABLE notification_channels (
    org_id         uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    id             uuid        NOT NULL,
    type           text        NOT NULL CHECK (type ~ '^[a-z][a-z0-9_]{0,31}$'),
    name           text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    language       text        NOT NULL CHECK (language IN ('de', 'en')),
    enabled        boolean     NOT NULL,
    settings       jsonb       NOT NULL DEFAULT '{}'
        CHECK (jsonb_typeof(settings) = 'object' AND octet_length(settings::text) <= 16384),
    secret_names   text[]      NOT NULL DEFAULT '{}' CHECK (cardinality(secret_names) <= 32),
    sealed_secrets bytea       NOT NULL CHECK (octet_length(sealed_secrets) <= 65536),
    created_at     timestamptz NOT NULL,
    updated_at     timestamptz NOT NULL,
    created_by     uuid,
    PRIMARY KEY (org_id, id)
);
ALTER TABLE notification_channels ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_channels FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON notification_channels
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON notification_channels TO sermon_app;

CREATE TABLE notification_deliveries (
    org_id          uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    id              bigint      GENERATED ALWAYS AS IDENTITY,
    -- No foreign key: the log of a deleted channel stays.
    channel_id      uuid        NOT NULL,
    notification    jsonb       NOT NULL
        CHECK (jsonb_typeof(notification) = 'object' AND octet_length(notification::text) <= 16384),
    test            boolean     NOT NULL DEFAULT false,
    status          text        NOT NULL CHECK (status IN ('pending', 'sending', 'sent', 'failed')),
    attempts        integer     NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 100),
    created_at      timestamptz NOT NULL,
    next_attempt_at timestamptz NOT NULL,
    claimed_until   timestamptz,
    last_attempt_at timestamptz,
    sent_at         timestamptz,
    last_error      text        NOT NULL DEFAULT '' CHECK (octet_length(last_error) <= 1000),
    PRIMARY KEY (org_id, id),
    CHECK ((status = 'sending') = (claimed_until IS NOT NULL))
);
CREATE INDEX notification_deliveries_due_idx ON notification_deliveries (org_id, next_attempt_at)
    WHERE status IN ('pending', 'sending');
CREATE INDEX notification_deliveries_channel_idx ON notification_deliveries (org_id, channel_id, id DESC);
CREATE INDEX notification_deliveries_created_idx ON notification_deliveries (org_id, created_at);
ALTER TABLE notification_deliveries ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_deliveries FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON notification_deliveries
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON notification_deliveries TO sermon_app;

-- +goose Down
DROP TABLE notification_deliveries;
DROP TABLE notification_channels;
