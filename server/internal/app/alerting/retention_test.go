package alerting_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/alerting"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

type purger struct {
	orgs   []tenant.OrgID
	before map[tenant.OrgID]time.Time
	fail   tenant.OrgID
}

func (p *purger) Organizations(context.Context) ([]tenant.OrgID, error) { return p.orgs, nil }

func (p *purger) PurgeHistory(_ context.Context, scope tenant.Scope, before time.Time) (int, error) {
	if scope.OrgID() == p.fail {
		return 0, errors.New("database down")
	}
	p.before[scope.OrgID()] = before
	return 2, nil
}

// Every organization keeps a year of history; one failing organization does not stop the others.
func TestPurgeHistory_KeepsAYearPerOrganization(t *testing.T) {
	p := &purger{orgs: []tenant.OrgID{orgA, orgB}, before: map[tenant.OrgID]time.Time{}, fail: orgA}
	n, err := alerting.PurgeHistory(context.Background(), p, t0)
	if err == nil || n != 2 || !p.before[orgB].Equal(t0.Add(-alerting.HistoryRetention)) {
		t.Errorf("purged %d, %v, before %v", n, err, p.before)
	}
	if alerting.HistoryRetention != 365*24*time.Hour {
		t.Errorf("retention %v, want a year like the security events", alerting.HistoryRetention)
	}
}
