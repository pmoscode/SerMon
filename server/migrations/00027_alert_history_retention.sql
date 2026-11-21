-- Retention of the alert history (M2-16): it holds the fields of security alerts (user, address)
-- and is kept at most as long as the security events, one year. The application may delete
-- only what is older: a restrictive policy is ANDed with the tenant policy, so the recent
-- history stays append-only for sermon_app (ADR 0007, ADR 0019).

-- +goose Up
CREATE POLICY delete_only_expired ON alert_history AS RESTRICTIVE FOR DELETE
    USING (at < now() - interval '365 days');
GRANT DELETE ON alert_history TO sermon_app;

-- +goose Down
REVOKE DELETE ON alert_history FROM sermon_app;
DROP POLICY delete_only_expired ON alert_history;
