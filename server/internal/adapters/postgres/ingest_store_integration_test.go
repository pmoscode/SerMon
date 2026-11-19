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
	facts := ingest.HostFacts{Hostname: "db-02", OSID: "ubuntu", OSVersion: "24.04", KernelRelease: "6.8.0", Architecture: "arm64", CPUCount: 8, MemoryBytes: 16 << 30}

	group, err := store.Connected(ctx, tenant.OrgID(orgA), identity.NodeID(node), facts, "1.2.3", at)
	if err != nil || uuid.UUID(group) == uuid.Nil {
		t.Fatalf("Connected() = %v, %v", group, err)
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
	var hostname, arch, version string
	var cpus int32
	var seen time.Time
	err = conn.QueryRow(ctx, "SELECT hostname, architecture, cpu_count, agent_version, last_seen_at FROM nodes WHERE id = $1", node).
		Scan(&hostname, &arch, &cpus, &version, &seen)
	if err != nil || hostname != "db-02" || arch != "arm64" || cpus != 8 || version != "1.2.3" || !seen.Equal(at.Add(30*time.Second)) {
		t.Errorf("node = %s %s %d %s %v, %v", hostname, arch, cpus, version, seen, err)
	}

	// Another organization can neither update nor touch the node.
	if _, err := store.Connected(ctx, tenant.OrgID(orgB), identity.NodeID(node), facts, "6.6.6", at); !errors.Is(err, postgres.ErrNodeNotFound) {
		t.Errorf("Connected() from org B = %v, want ErrNodeNotFound", err)
	}
	if err := store.Seen(ctx, tenant.OrgID(orgB), identity.NodeID(node), at.Add(time.Hour)); !errors.Is(err, postgres.ErrNodeNotFound) {
		t.Errorf("Seen() from org B = %v, want ErrNodeNotFound", err)
	}
}
