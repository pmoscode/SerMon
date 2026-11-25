//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/app/ingest"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/secevent"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

func TestIngestNodeStore_RecordsHelloAndContactWithinTheOrganization(t *testing.T) {
	f := setup(t)
	db := open(t, f.appDSN)
	store := postgres.NewIngestNodeStore(db)
	ctx := context.Background()
	node := uuid.New()
	createNode(t, f.db.OwnerDSN, orgA, node)
	at := time.Now().Truncate(time.Second)
	facts := ingest.HostFacts{Hostname: "db-02", OSID: "ubuntu", OSVersion: "24.04", KernelRelease: "6.8.0", Architecture: "arm64", CPUCount: 8, MemoryBytes: 16 << 30, SecuritySource: secevent.SourceFileUnverified}

	connected, err := store.Connected(ctx, tenant.OrgID(orgA), identity.NodeID(node), facts, "1.2.3", at)
	if err != nil || uuid.UUID(connected.Group) == uuid.Nil || connected.Limits != (ingest.Limits{}) {
		t.Fatalf("Connected() = %+v, %v", connected, err)
	}
	if err := store.Seen(ctx, tenant.OrgID(orgA), identity.NodeID(node), at.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	// An older contact never moves last_seen_at back.
	if err := store.Seen(ctx, tenant.OrgID(orgA), identity.NodeID(node), at); err != nil {
		t.Fatal(err)
	}

	conn, err := pgx.Connect(ctx, f.db.OwnerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var hostname, arch, version, source string
	var cpus int32
	var seen time.Time
	err = conn.QueryRow(ctx, "SELECT hostname, architecture, cpu_count, agent_version, last_seen_at, security_source FROM nodes WHERE id = $1", node).
		Scan(&hostname, &arch, &cpus, &version, &seen, &source)
	if err != nil || hostname != "db-02" || arch != "arm64" || cpus != 8 || version != "1.2.3" || !seen.Equal(at.Add(30*time.Second)) || source != "file_unverified" {
		t.Errorf("node = %s %s %d %s %v %s, %v", hostname, arch, cpus, version, seen, source, err)
	}
	// Each Hello is a new session (M4-13: a node is back after a reboot); Seen doesn't move it.
	var connectedAt time.Time
	if err := conn.QueryRow(ctx, "SELECT connected_at FROM nodes WHERE id = $1", node).Scan(&connectedAt); err != nil || !connectedAt.Equal(at) {
		t.Errorf("connected_at = %v, %v; want %v", connectedAt, err, at)
	}
	// An older agent reports no source: it is unknown again, not left over from before (M2-31).
	facts.SecuritySource = secevent.SourceUnknown
	if _, err := store.Connected(ctx, tenant.OrgID(orgA), identity.NodeID(node), facts, "1.2.2", at); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, "SELECT security_source FROM nodes WHERE id = $1", node).Scan(&source); err != nil || source != "" {
		t.Errorf("source after an older agent = %q, %v", source, err)
	}
	// The column takes only known sources.
	if _, err := conn.Exec(ctx, "UPDATE nodes SET security_source = 'syslog' WHERE id = $1", node); err == nil {
		t.Error("unknown security source stored")
	}
	// The ingest quotas of the organization come with the connection (M2-32).
	for _, q := range []string{
		"SELECT set_config('sermon.org_id', '" + orgA.String() + "', false)",
		"UPDATE organizations SET samples_per_second = 500, series_per_node = 300 WHERE id = '" + orgA.String() + "'",
	} {
		if _, err := conn.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		_, _ = conn.Exec(ctx, "UPDATE organizations SET samples_per_second = NULL, series_per_node = NULL WHERE id = '"+orgA.String()+"'")
	}()
	connected, err = store.Connected(ctx, tenant.OrgID(orgA), identity.NodeID(node), facts, "1.2.3", at)
	if err != nil || connected.Limits != (ingest.Limits{SamplesPerSecond: 500, SeriesPerNode: 300}) {
		t.Errorf("Connected() with quotas = %+v, %v", connected, err)
	}

	// Another organization can neither update nor touch the node.
	if _, err := store.Connected(ctx, tenant.OrgID(orgB), identity.NodeID(node), facts, "6.6.6", at); !errors.Is(err, postgres.ErrNodeNotFound) {
		t.Errorf("Connected() from org B = %v, want ErrNodeNotFound", err)
	}
	if err := store.Seen(ctx, tenant.OrgID(orgB), identity.NodeID(node), at.Add(time.Hour)); !errors.Is(err, postgres.ErrNodeNotFound) {
		t.Errorf("Seen() from org B = %v, want ErrNodeNotFound", err)
	}
}
