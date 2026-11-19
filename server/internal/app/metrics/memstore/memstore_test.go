package memstore_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/app/metrics/memstore"
	"github.com/pmoscode/SerMon/server/internal/app/metrics/metricstoretest"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

type fixture struct{ store *memstore.Store }

func (f fixture) Node(_ *testing.T, org tenant.OrgID, group tenant.GroupID) identity.NodeID {
	id := identity.NodeID(uuid.New())
	f.store.AddNode(org, group, id)
	return id
}

func (fixture) Settle(*testing.T) {}

func TestMemoryStore_Contract(t *testing.T) {
	metricstoretest.Run(t, func(*testing.T) (metrics.Store, metricstoretest.Fixture) {
		store := memstore.New()
		return store, fixture{store}
	})
}
