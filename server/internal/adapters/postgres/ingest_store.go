package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/ingest"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// ErrNodeNotFound means the node is not part of the organization.
var ErrNodeNotFound = errors.New("node not found in the organization")

// IngestNodeStore records the agents' Hello and contact on the node records (M1-24).
type IngestNodeStore struct{ db *DB }

var _ ingest.Nodes = (*IngestNodeStore)(nil)

// NewIngestNodeStore returns the store.
func NewIngestNodeStore(db *DB) *IngestNodeStore { return &IngestNodeStore{db: db} }

// Connected implements ingest.Nodes.
func (s *IngestNodeStore) Connected(ctx context.Context, org tenant.OrgID, node identity.NodeID, facts ingest.HostFacts, agentVersion string, at time.Time) (ingest.Connection, error) {
	scope, err := tenant.ForAllGroups(org)
	if err != nil {
		return ingest.Connection{}, fmt.Errorf("node connected: %w", err)
	}
	var row sqlcdb.UpdateNodeOnConnectRow
	err = s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		row, err = q.UpdateNodeOnConnect(ctx, sqlcdb.UpdateNodeOnConnectParams{
			Hostname: facts.Hostname, OsID: facts.OSID, OsVersion: facts.OSVersion, KernelRelease: facts.KernelRelease,
			Architecture: facts.Architecture, CpuCount: int32(min(facts.CPUCount, math.MaxInt32)),
			MemoryBytes:  int64(min(facts.MemoryBytes, math.MaxInt64)),
			AgentVersion: pgtype.Text{String: agentVersion, Valid: true}, SeenAt: &at, ID: uuid.UUID(node),
			SecuritySource: string(facts.SecuritySource),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNodeNotFound
		}
		if err != nil {
			return fmt.Errorf("update node: %w", err)
		}
		return nil
	})
	if err != nil {
		return ingest.Connection{}, fmt.Errorf("node connected: %w", err)
	}
	return ingest.Connection{
		Group:  tenant.GroupID(row.GroupID),
		Limits: ingest.Limits{SamplesPerSecond: quotaOf(row.SamplesPerSecond), SeriesPerNode: quotaOf(row.SeriesPerNode)},
	}, nil
}

// Seen implements ingest.Nodes.
func (s *IngestNodeStore) Seen(ctx context.Context, org tenant.OrgID, node identity.NodeID, at time.Time) error {
	scope, err := tenant.ForAllGroups(org)
	if err != nil {
		return fmt.Errorf("node seen: %w", err)
	}
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		n, err := q.TouchNode(ctx, sqlcdb.TouchNodeParams{SeenAt: &at, ID: uuid.UUID(node)})
		if err != nil {
			return fmt.Errorf("touch node: %w", err)
		}
		if n == 0 {
			return ErrNodeNotFound
		}
		return nil
	})
}
