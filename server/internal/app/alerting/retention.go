package alerting

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// HistoryRetention is how long the alert history (with the fields of security alerts) is kept:
// as long as the security events (KONZEPT §3, M2-16).
const HistoryRetention = 365 * 24 * time.Hour

// HistoryPurger deletes old history per organization.
type HistoryPurger interface {
	Organizations(ctx context.Context) ([]tenant.OrgID, error)
	PurgeHistory(ctx context.Context, scope tenant.Scope, before time.Time) (int, error)
}

// PurgeHistory deletes the history older than HistoryRetention in every organization and
// returns how many entries went; a failing organization does not stop the others.
func PurgeHistory(ctx context.Context, p HistoryPurger, now time.Time) (int, error) {
	orgs, err := p.Organizations(ctx)
	if err != nil {
		return 0, fmt.Errorf("list organizations: %w", err)
	}
	total := 0
	var errs []error
	for _, org := range orgs {
		scope, err := tenant.ForAllGroups(org)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		n, err := p.PurgeHistory(ctx, scope, now.Add(-HistoryRetention))
		if err != nil {
			errs = append(errs, fmt.Errorf("organization %s: %w", org, err))
			continue
		}
		total += n
	}
	return total, errors.Join(errs...)
}

// Maintenance purges the history every interval until ctx ends.
func Maintenance(ctx context.Context, p HistoryPurger, now func() time.Time, interval time.Duration, logger *slog.Logger) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if n, err := PurgeHistory(ctx, p, now()); err != nil {
			logger.ErrorContext(ctx, "alert history: retention failed", "error", err.Error())
		} else if n > 0 {
			logger.InfoContext(ctx, "alert history deleted (retention)", "count", n)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
