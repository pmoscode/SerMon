//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/pgtest"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/nodelife"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// seedNodeData gives a node of the organization rows in every table a node's data lives in:
// series with raw, aggregated and anomalous points, a security event, alert instance and history,
// a known login source, a certificate.
func seedNodeData(t *testing.T, ownerDSN string, org, node uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	var group uuid.UUID
	if err := conn.QueryRow(ctx, "SELECT id FROM node_groups WHERE org_id = $1 LIMIT 1", org).Scan(&group); err != nil {
		t.Fatal(err)
	}
	exec("SELECT set_config('sermon.org_id', $1, false)", org.String())
	exec(`INSERT INTO nodes (id, org_id, group_id, hostname, machine_id, os_id, os_version, kernel_release, architecture, cpu_count, memory_bytes)
	      VALUES ($1, $2, $3, $4, $5, 'debian', '13', '6.12.0', 'amd64', 2, 1073741824)`,
		node, org, group, "host-"+node.String()[:8], strings.ReplaceAll(node.String(), "-", ""))
	exec("INSERT INTO series (org_id, node_id, metric) VALUES ($1, $2, 'sermon_purge_test')", org, node)
	exec(`INSERT INTO metrics_raw (ts, series_id, avg, min, max)
	      SELECT now() - interval '5 minutes', id, 1, 1, 1 FROM series WHERE node_id = $1`, node)
	exec(`INSERT INTO metrics_raw (ts, series_id, avg, min, max)
	      SELECT now() - interval '3 hours', id, 1, 1, 1 FROM series WHERE node_id = $1`, node)
	exec(`INSERT INTO anomaly_points (ts, series_id, score, anomalous) SELECT now(), id, 5, true FROM series WHERE node_id = $1`, node)
	exec(`INSERT INTO anomaly_baselines (org_id, series_id, hour, median, mad, low, high, points, computed_at)
	      SELECT $1, id, 0, 1, 0, 1, 1, 30, now() FROM series WHERE node_id = $2`, org, node)
	exec(`INSERT INTO security_events (org_id, node_id, occurred_at, kind, user_name) VALUES ($1, $2, now(), 'ssh.login.failed', 'alice')`, org, node)
	exec(`INSERT INTO alert_instances (org_id, rule_id, node_id, state, since) VALUES ($1, 'system.cpu.usage_high', $2, 2, now())`, org, node)
	exec(`INSERT INTO alert_history (org_id, rule_id, node_id, kind, from_state, to_state, at, value)
	      VALUES ($1, 'system.cpu.usage_high', $2, 1, 0, 2, now(), 0.9)`, org, node)
	exec(`INSERT INTO known_login_sources (org_id, node_id, user_name, source, first_seen, last_seen)
	      VALUES ($1, $2, 'alice', '192.0.2.10', now(), now())`, org, node)
	var authority int32
	if err := conn.QueryRow(ctx, `INSERT INTO pki_authorities (root_cert, root_key_sealed, intermediate_cert, intermediate_key_sealed, active)
		VALUES ('\x00', '\x00', '\x00', '\x00', false) RETURNING id`).Scan(&authority); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO agent_certificates (serial, org_id, node_id, authority_id, not_before, not_after)
	      VALUES ($1, $2, $3, $4, now() - interval '1 hour', now() + interval '1 day')`, strings.ReplaceAll(uuid.NewString(), "-", ""), org, node, authority)
	exec("CALL refresh_continuous_aggregate('metrics_1m', now() - interval '4 hours', now())")
	exec("CALL refresh_continuous_aggregate('metrics_1h', now() - interval '5 hours', now() - interval '1 hour')")
}

// compressAggregates moves every chunk of the aggregates to the columnstore (V1-21): the purge
// deletes from compressed chunks as the policies leave them after a few hours.
func compressAggregates(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	ctx := context.Background()
	for _, view := range []string{"metrics_1m", "metrics_1h"} {
		if _, err := conn.Exec(ctx, "SELECT compress_chunk(c, recompress => true) FROM show_chunks('"+view+"') c"); err != nil {
			t.Fatalf("compress %s: %v", view, err)
		}
		var open int
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM timescaledb_information.chunks c
			JOIN timescaledb_information.continuous_aggregates a
			  ON a.materialization_hypertable_schema = c.hypertable_schema AND a.materialization_hypertable_name = c.hypertable_name
			WHERE a.view_name = $1 AND NOT c.is_compressed`, view).Scan(&open); err != nil || open != 0 {
			t.Fatalf("%s: %d chunks not compressed, %v", view, open, err)
		}
	}
}

// nodeRows counts the rows of the node in every table with a node_id column and its points in
// the tables keyed by series.
func nodeRows(t *testing.T, conn *pgx.Conn, node uuid.UUID, series []int64) map[string]int64 {
	t.Helper()
	ctx := context.Background()
	rows, err := conn.Query(ctx, `SELECT c.table_name FROM information_schema.columns c
		JOIN information_schema.tables t ON t.table_schema = c.table_schema AND t.table_name = c.table_name
		WHERE c.table_schema = 'public' AND c.column_name = 'node_id' AND t.table_type = 'BASE TABLE'
		  AND NOT c.table_name LIKE 'security_events\_%'`)
	if err != nil {
		t.Fatal(err)
	}
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	count := func(name, query string, arg any) {
		var n int64
		if err := conn.QueryRow(ctx, query, arg).Scan(&n); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out[name] = n
	}
	for _, table := range tables {
		count(table, "SELECT count(*) FROM "+pgx.Identifier{table}.Sanitize()+" WHERE node_id = $1", node)
	}
	count("nodes", "SELECT count(*) FROM nodes WHERE id = $1", node)
	for _, table := range []string{"metrics_raw", "metrics_1m", "metrics_1h", "anomaly_points", "anomaly_baselines"} {
		count(table, "SELECT count(*) FROM "+table+" WHERE series_id = ANY($1)", series)
	}
	return out
}

func nodeSeries(t *testing.T, conn *pgx.Conn, node uuid.UUID) []int64 {
	t.Helper()
	rows, err := conn.Query(context.Background(), "SELECT id FROM series WHERE node_id = $1", node)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

// M5-29 (ADR 0041): the job purges a deactivated node that is due with everything it left in
// the database, writes node.purge into the organization's audit log, and touches neither the
// other node of the organization nor the same data of another organization. Nodes that are only
// deactivated, or not due yet, stay.
func TestNodePurge_RemovesTheNodesDataAndNothingElse(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p := seedPurgeOrg(t, f.db.OwnerDSN, uuid.Nil)
	other := seedPurgeOrg(t, f.db.OwnerDSN, uuid.Nil)
	gone, sibling, elsewhere := uuid.New(), uuid.New(), uuid.New()
	seedNodeData(t, f.db.OwnerDSN, p.org, gone)
	seedNodeData(t, f.db.OwnerDSN, p.org, sibling)
	seedNodeData(t, f.db.OwnerDSN, other.org, elsewhere)
	owner, err := pgx.Connect(ctx, pgtest.Shared(t).OwnerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close(ctx) }()
	compressAggregates(t, owner)
	series := map[uuid.UUID][]int64{}
	before := map[uuid.UUID]map[string]int64{}
	for _, n := range []uuid.UUID{gone, sibling, elsewhere} {
		series[n] = nodeSeries(t, owner, n)
		before[n] = nodeRows(t, owner, n, series[n])
	}
	for _, table := range []string{
		"nodes", "series", "metrics_raw", "metrics_1m", "metrics_1h", "anomaly_points", "anomaly_baselines", "security_events",
		"alert_instances", "alert_history", "known_login_sources", "agent_certificates",
	} {
		if before[gone][table] < 1 {
			t.Fatalf("fixture incomplete: %s = %v", table, before[gone])
		}
	}

	pg := open(t, f.appDSN)
	store := postgres.NewNodeLifecycleStore(pg)
	now := time.Now()
	purge := func(node uuid.UUID) error { return store.Purge(ctx, tenant.OrgID(p.org), identity.NodeID(node), now) }
	if err := purge(gone); err == nil || !strings.Contains(err.Error(), "not deactivated and due") {
		t.Fatalf("purge of an active node: %v", err)
	}
	if err := changeNode(ctx, store, p.org, gone, nodelife.OpScheduleDeletion, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if marked, err := store.StartPurge(ctx, tenant.OrgID(p.org), identity.NodeID(gone), now); err != nil || marked {
		t.Fatalf("marked before the deadline: %v, %v", marked, err)
	}
	if err := purge(gone); err == nil {
		t.Fatal("purge before the deadline succeeded")
	}
	// Due, but not marked by the job: refused (its logs may still be there).
	if err := store.Purge(ctx, tenant.OrgID(p.org), identity.NodeID(gone), now.Add(2*time.Hour)); err == nil {
		t.Fatal("purge of an unmarked node succeeded")
	}
	// The same node ID through another organization: neither marked nor purged.
	if marked, err := store.StartPurge(ctx, tenant.OrgID(other.org), identity.NodeID(gone), now.Add(2*time.Hour)); err != nil || marked {
		t.Fatalf("another organization marked the node: %v, %v", marked, err)
	}
	if err := changeNode(ctx, store, p.org, sibling, nodelife.OpDeactivate, now, time.Time{}); err != nil {
		t.Fatal(err)
	}
	due, err := store.DueForDeletion(ctx, now.Add(2*time.Hour), nodelife.PurgeBatch)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0] != (nodelife.Due{Org: tenant.OrgID(p.org), Node: identity.NodeID(gone)}) {
		t.Fatalf("due = %v, want only the scheduled node", due)
	}
	if marked, err := store.StartPurge(ctx, due[0].Org, due[0].Node, now.Add(2*time.Hour)); err != nil || !marked {
		t.Fatalf("mark the due node: %v, %v", marked, err)
	}
	if err := store.Purge(ctx, tenant.OrgID(other.org), identity.NodeID(gone), now.Add(2*time.Hour)); err == nil {
		t.Fatal("another organization purged the node")
	}
	if err := store.Purge(ctx, due[0].Org, due[0].Node, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}

	for table, n := range nodeRows(t, owner, gone, series[gone]) {
		if n != 0 {
			t.Errorf("%s: %d rows of the deleted node left", table, n)
		}
	}
	for _, n := range []uuid.UUID{sibling, elsewhere} {
		for table, count := range nodeRows(t, owner, n, series[n]) {
			if count != before[n][table] {
				t.Errorf("%s of node %s: %d rows, had %d", table, n, count, before[n][table])
			}
		}
	}
	var action, details string
	if err := owner.QueryRow(ctx, "SELECT action, details::text FROM audit_log WHERE org_id = $1 AND target = $2 ORDER BY seq DESC LIMIT 1",
		p.org, "node:"+gone.String()).Scan(&action, &details); err != nil || action != "node.purge" || !strings.Contains(details, "host-") {
		t.Errorf("audit = %q %s (%v), want node.purge with the host name", action, details, err)
	}
}

// Deactivating revokes every certificate of the node and the definer check refuses it even with
// a certificate not revoked; the takeover window closes.
func TestNodeLifecycle_DeactivationRefusesTheAgent(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p := seedPurgeOrg(t, f.db.OwnerDSN, uuid.Nil)
	node := uuid.New()
	seedNodeData(t, f.db.OwnerDSN, p.org, node)
	app, err := pgx.Connect(ctx, f.appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close(ctx) }()
	var serial string
	ownerQueryRowPurge(t, f.db.OwnerDSN, "SELECT serial FROM agent_certificates WHERE node_id = '"+node.String()+"'", &serial)
	usable := func() bool {
		t.Helper()
		var ok bool
		if err := app.QueryRow(ctx, "SELECT agent_certificate_usable($1, $2, $3, now())", serial, p.org, node).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !usable() {
		t.Fatal("the certificate is not usable before")
	}
	ownerExec(t, f.db.OwnerDSN, "UPDATE nodes SET takeover_until = now() + interval '1 hour' WHERE id = $1", node)
	if err := changeNode(ctx, postgres.NewNodeLifecycleStore(open(t, f.appDSN)), p.org, node, nodelife.OpDeactivate, time.Now(), time.Time{}); err != nil {
		t.Fatal(err)
	}
	if usable() {
		t.Error("the certificate of a deactivated node is usable")
	}
	var revoked, window bool
	ownerQueryRowPurge(t, f.db.OwnerDSN, "SELECT revoked_at IS NOT NULL FROM agent_certificates WHERE serial = '"+serial+"'", &revoked)
	ownerQueryRowPurge(t, f.db.OwnerDSN, "SELECT takeover_until IS NOT NULL FROM nodes WHERE id = '"+node.String()+"'", &window)
	if !revoked || window {
		t.Errorf("revoked %v, takeover window open %v", revoked, window)
	}
	// Even un-revoked by hand, the deactivation refuses it.
	ownerExec(t, f.db.OwnerDSN, "UPDATE agent_certificates SET revoked_at = NULL WHERE serial = $1", serial)
	if usable() {
		t.Error("deactivated node with an unrevoked certificate is usable")
	}
}

// Cancelling while the job starts: the job marks the node under its row lock, so it sees the
// cancellation (and leaves the node and its logs) or the cancellation waits and is refused.
func TestNodePurge_WaitsForAConcurrentCancel(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p := seedPurgeOrg(t, f.db.OwnerDSN, uuid.Nil)
	node := uuid.New()
	seedNodeData(t, f.db.OwnerDSN, p.org, node)
	ownerExec(t, f.db.OwnerDSN, "UPDATE nodes SET deactivated_at = now(), deletion_due_at = now() - interval '1 minute' WHERE id = $1", node)
	cancel, err := pgx.Connect(ctx, f.db.OwnerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cancel.Close(ctx) }()
	tx, err := cancel.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "UPDATE nodes SET deletion_due_at = NULL WHERE id = $1", node); err != nil {
		t.Fatal(err)
	}
	type result struct {
		marked bool
		err    error
	}
	started := make(chan result, 1)
	store := postgres.NewNodeLifecycleStore(open(t, f.appDSN))
	go func() {
		marked, err := store.StartPurge(ctx, tenant.OrgID(p.org), identity.NodeID(node), time.Now())
		started <- result{marked, err}
	}()
	select {
	case r := <-started:
		t.Fatalf("marking did not wait for the cancel: %+v", r)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if r := <-started; r.err != nil || r.marked {
		t.Errorf("after the cancel: %+v, want not marked", r)
	}
	if err := store.Purge(ctx, tenant.OrgID(p.org), identity.NodeID(node), time.Now()); err == nil {
		t.Error("node_purge deleted a node whose deletion was just cancelled")
	}
	var left int
	ownerQueryRowPurge(t, f.db.OwnerDSN, "SELECT count(*) FROM nodes WHERE id = '"+node.String()+"'", &left)
	if left != 1 {
		t.Error("the node is gone")
	}
}

// M5-33: once marked, the deletion can't be cancelled; node_purge only works in the caller's
// tenant, whatever organization it is given.
func TestNodePurge_MarkedNodesStayDueAndPurgeStaysInTheTenant(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p := seedPurgeOrg(t, f.db.OwnerDSN, uuid.Nil)
	other := seedPurgeOrg(t, f.db.OwnerDSN, uuid.Nil)
	node := uuid.New()
	seedNodeData(t, f.db.OwnerDSN, p.org, node)
	store := postgres.NewNodeLifecycleStore(open(t, f.appDSN))
	now := time.Now()
	if err := changeNode(ctx, store, p.org, node, nodelife.OpScheduleDeletion, now.Add(-2*time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if marked, err := store.StartPurge(ctx, tenant.OrgID(p.org), identity.NodeID(node), now); err != nil || !marked {
		t.Fatalf("mark: %v, %v", marked, err)
	}
	var seen *time.Time
	err := store.Change(ctx, scopeFor(t, p.org), identity.NodeID(node), func(st nodelife.State) (nodelife.Change, error) {
		seen = st.PurgeStartedAt
		return nodelife.Change{Op: nodelife.OpCancelDeletion, Audit: access.AuditEntry{Action: "node.cancel_deletion", Target: "node:" + node.String()}}, nil
	})
	if seen == nil || !errors.Is(err, nodelife.ErrNotScheduled) {
		t.Errorf("cancel of a marked node: state marked %v, %v; want refused", seen, err)
	}
	app, err := pgx.Connect(ctx, f.appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close(ctx) }()
	tx, err := app.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT set_config('sermon.org_id', $1, true)", other.org.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "SELECT node_purge($1, $2, now())", p.org, node); err == nil || !strings.Contains(err.Error(), "outside the tenant") {
		t.Errorf("node_purge from another tenant: %v", err)
	}
}

// M5-33: check and change happen in one transaction: a node moved out of the admin's groups
// while the change waits for its lock is not found, and nothing changes.
func TestNodeLifecycle_ChangeSeesAConcurrentGroupMove(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p := seedPurgeOrg(t, f.db.OwnerDSN, uuid.Nil)
	node := uuid.New()
	seedNodeData(t, f.db.OwnerDSN, p.org, node)
	var group uuid.UUID
	ownerQueryRowPurge(t, f.db.OwnerDSN, "SELECT group_id FROM nodes WHERE id = '"+node.String()+"'", &group)
	elsewhere := uuid.New()
	ownerExec(t, f.db.OwnerDSN, "INSERT INTO node_groups (id, org_id, name) VALUES ($1, $2, 'db')", elsewhere, p.org)
	owner, err := pgx.Connect(ctx, f.db.OwnerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close(ctx) }()
	move, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := move.Exec(ctx, "SELECT set_config('sermon.org_id', $1, true)", p.org.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := move.Exec(ctx, "UPDATE nodes SET group_id = $1 WHERE id = $2", elsewhere, node); err != nil {
		t.Fatal(err)
	}
	scope, err := tenant.ForGroups(tenant.OrgID(p.org), tenant.GroupID(group))
	if err != nil {
		t.Fatal(err)
	}
	store := postgres.NewNodeLifecycleStore(open(t, f.appDSN))
	done := make(chan error, 1)
	go func() {
		done <- store.Change(ctx, scope, identity.NodeID(node), func(nodelife.State) (nodelife.Change, error) {
			return nodelife.Change{Op: nodelife.OpDeactivate, At: time.Now(), Audit: access.AuditEntry{Action: "node.deactivate", Target: "node:" + node.String()}}, nil
		})
	}()
	select {
	case err := <-done:
		t.Fatalf("the change did not wait for the move: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := move.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, access.ErrNotFound) {
		t.Errorf("change after the node moved away: %v, want NOT_FOUND", err)
	}
	var deactivated bool
	ownerQueryRowPurge(t, f.db.OwnerDSN, "SELECT deactivated_at IS NOT NULL FROM nodes WHERE id = '"+node.String()+"'", &deactivated)
	if deactivated {
		t.Error("the node of another group was deactivated")
	}
}

// changeNode makes a lifecycle change as an admin of all groups would.
func changeNode(ctx context.Context, store *postgres.NodeLifecycleStore, org, node uuid.UUID, op nodelife.Op, at, due time.Time) error {
	scope, err := tenant.ForAllGroups(tenant.OrgID(org))
	if err != nil {
		return err
	}
	return store.Change(ctx, scope, identity.NodeID(node), func(nodelife.State) (nodelife.Change, error) {
		return nodelife.Change{Op: op, At: at, Due: due, Audit: access.AuditEntry{Action: "node.change", Target: "node:" + node.String()}}, nil
	})
}
