-- Known login sources (M3-16, KONZEPT §11): which user logged in on a node from which source (the
-- address, IPv6 by its /64, else the host name sshd logged). The ingest records every successful
-- login and marks the first of a user and source as new_source; the rule
-- security.ssh.login_new_source alerts on those once SerMon has learned a node for a week.
--
-- The table holds personal data like the events: a source unused for a year is forgotten (the
-- ingest deletes those of a node when a login of it arrives) and a node holds at most 10,000
-- (forged lines from classic log files must not fill the table, ADR 0025). The rows go with the
-- node.

-- +goose Up
ALTER TABLE security_events ADD COLUMN new_source boolean NOT NULL DEFAULT false;

CREATE TABLE known_login_sources (
    org_id     uuid        NOT NULL,
    node_id    uuid        NOT NULL,
    user_name  text        NOT NULL CHECK (octet_length(user_name) <= 128),
    source     text        NOT NULL CHECK (octet_length(source) BETWEEN 1 AND 253),
    first_seen timestamptz NOT NULL,
    last_seen  timestamptz NOT NULL,
    logins     bigint      NOT NULL DEFAULT 1 CHECK (logins >= 1),
    PRIMARY KEY (org_id, node_id, user_name, source),
    FOREIGN KEY (org_id, node_id) REFERENCES nodes (org_id, id) ON DELETE CASCADE
);
ALTER TABLE known_login_sources ENABLE ROW LEVEL SECURITY;
ALTER TABLE known_login_sources FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON known_login_sources
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON known_login_sources TO sermon_app;

-- When SerMon began to learn a node's login sources: its enrollment, for the nodes before this
-- migration now (the stored events can't be read here: row-level security binds the migration
-- role as well). The rule waits a week from it, so an upgrade doesn't report every next login.
ALTER TABLE nodes ADD COLUMN learning_since timestamptz NOT NULL DEFAULT now();

-- +goose Down
ALTER TABLE nodes DROP COLUMN learning_since;
DROP TABLE known_login_sources;
ALTER TABLE security_events DROP COLUMN new_source;
