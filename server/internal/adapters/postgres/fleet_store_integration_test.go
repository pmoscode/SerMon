//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

func TestFleetStore_ListsOnlyNodesOfTheScope(t *testing.T) {
	f := setup(t)
	store := postgres.NewFleetStore(open(t, f.appDSN))
	ctx := context.Background()
	nodeA, nodeB := uuid.New(), uuid.New()
	createNode(t, f.db.OwnerDSN, orgA, nodeA)
	createNode(t, f.db.OwnerDSN, orgB, nodeB)

	all, err := tenant.ForAllGroups(tenant.OrgID(orgA))
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := store.List(ctx, all)
	if err != nil {
		t.Fatal(err)
	}
	var group tenant.GroupID
	found := false
	for _, n := range nodes {
		if n.ID == identity.NodeID(nodeB) {
			t.Errorf("org A lists a node of org B")
		}
		if n.ID == identity.NodeID(nodeA) {
			found, group = true, n.Group
			if n.GroupName == "" || n.Hostname != "host" || n.CPUCount != 2 {
				t.Errorf("node = %+v", n)
			}
		}
	}
	if !found {
		t.Fatal("own node missing")
	}

	otherGroup, _ := tenant.ForGroups(tenant.OrgID(orgA), tenant.GroupID(uuid.New()))
	if restricted, err := store.List(ctx, otherGroup); err != nil || len(restricted) != 0 {
		t.Errorf("scope of another group lists %+v, %v", restricted, err)
	}
	if ok, err := store.HasGroup(ctx, all, group); err != nil || !ok {
		t.Errorf("HasGroup(own) = %v, %v", ok, err)
	}
	allB, _ := tenant.ForAllGroups(tenant.OrgID(orgB))
	if ok, err := store.HasGroup(ctx, allB, group); err != nil || ok {
		t.Errorf("HasGroup(foreign) = %v, %v", ok, err)
	}
}
