-- Updates of the nodes (M4-09, KONZEPT §14.1): the latest report of a node and its packages.
--
-- node_update_state holds one row per node, replaced by a report collected later (the agent's
-- clock, bounded by the ingest; a resend or an older batch changes nothing). total_count and
-- security_count count all updates, also those the agent (5,000) or the organization's limit
-- left out of node_update_packages; packages_stored says how many are there.
--
-- node_update_packages are the listed updates. first_seen_at is when a report first listed an
-- update (name and candidate version) of the node; it stays while later reports list it, so
-- oldest_security_at in the state tells how long security updates wait. Names, versions and
-- sources come from the host's package sources (a third party): bounded here, without invisible
-- characters from the ingest, shown as text only. The rows go with the node.

-- +goose Up
CREATE TABLE node_update_state (
    org_id             uuid        NOT NULL,
    node_id            uuid        NOT NULL,
    collected_at       timestamptz NOT NULL,
    received_at        timestamptz NOT NULL,
    availability       smallint    NOT NULL CHECK (availability BETWEEN 1 AND 5),
    reason             text        NOT NULL DEFAULT '' CHECK (octet_length(reason) <= 1024),
    package_manager    smallint    NOT NULL CHECK (package_manager BETWEEN 0 AND 3),
    total_count        integer     NOT NULL CHECK (total_count BETWEEN 0 AND 1000000),
    security_count     integer     NOT NULL CHECK (security_count BETWEEN 0 AND total_count),
    packages_stored    integer     NOT NULL CHECK (packages_stored BETWEEN 0 AND least(total_count, 5000)),
    reboot_required    boolean     NOT NULL,
    reboot_reason      text        NOT NULL DEFAULT '' CHECK (octet_length(reboot_reason) <= 1024),
    last_upgrade_at    timestamptz,
    -- What /etc/sermon/helper.toml allows; NULL without helper.
    allow_remote_updates boolean,
    allow_remote_reboot  boolean,
    allow_self_update    boolean,
    refreshed          boolean     NOT NULL,
    oldest_security_at timestamptz,
    PRIMARY KEY (org_id, node_id),
    FOREIGN KEY (org_id, node_id) REFERENCES nodes (org_id, id) ON DELETE CASCADE,
    CHECK ((allow_remote_updates IS NULL) = (allow_remote_reboot IS NULL)
       AND (allow_remote_updates IS NULL) = (allow_self_update IS NULL))
);
ALTER TABLE node_update_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE node_update_state FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON node_update_state
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT, UPDATE ON node_update_state TO sermon_app;

CREATE TABLE node_update_packages (
    org_id            uuid        NOT NULL,
    node_id           uuid        NOT NULL,
    name              text        NOT NULL CHECK (octet_length(name) BETWEEN 1 AND 256),
    candidate_version text        NOT NULL CHECK (octet_length(candidate_version) <= 256),
    installed_version text        NOT NULL CHECK (octet_length(installed_version) <= 256),
    source            text        NOT NULL CHECK (octet_length(source) <= 256),
    security          boolean     NOT NULL,
    first_seen_at     timestamptz NOT NULL,
    PRIMARY KEY (org_id, node_id, name, candidate_version),
    FOREIGN KEY (org_id, node_id) REFERENCES node_update_state (org_id, node_id) ON DELETE CASCADE
);
ALTER TABLE node_update_packages ENABLE ROW LEVEL SECURITY;
ALTER TABLE node_update_packages FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON node_update_packages
    USING (org_id = current_org_id()) WITH CHECK (org_id = current_org_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON node_update_packages TO sermon_app;

-- +goose Down
DROP TABLE node_update_packages;
DROP TABLE node_update_state;
