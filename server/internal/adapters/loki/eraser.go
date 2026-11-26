package loki

import (
	"context"
	"fmt"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/nodelife"
	"github.com/pmoscode/SerMon/server/internal/app/organization"
	"github.com/pmoscode/SerMon/server/internal/app/orgretention"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/logql"
	"github.com/pmoscode/SerMon/server/internal/domain/retention"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Eraser deletes an organization's logs when it is purged (M3-10, D29).
type Eraser struct{ client *Client }

var (
	_ organization.LogEraser = (*Eraser)(nil)
	_ nodelife.LogEraser     = (*Eraser)(nil)
	_ orgretention.LogEraser = (*Eraser)(nil)
)

// NewEraser returns the eraser.
func NewEraser(client *Client) *Eraser { return &Eraser{client: client} }

// EraseOrganization records the deletion of every line of the organization's tenant; the
// compactor runs it after the cancel period (10 minutes), the retention would after 14 days.
func (e *Eraser) EraseOrganization(ctx context.Context, org tenant.OrgID) error {
	if err := e.client.Delete(ctx, org, logql.Everything(), DeleteFrom, time.Now()); err != nil {
		return fmt.Errorf("erase the logs of %s: %w", org, err)
	}
	return nil
}

// EraseNode records the deletion of every line of the node (M5-29, ADR 0041); the compactor runs
// it after the cancel period.
func (e *Eraser) EraseNode(ctx context.Context, org tenant.OrgID, node identity.NodeID) error {
	if err := e.client.Delete(ctx, org, logql.MustSelect(node), DeleteFrom, time.Now()); err != nil {
		return fmt.Errorf("erase the logs of node %s: %w", node, err)
	}
	return nil
}

// EraseBefore records the deletion of the organization's lines before before (its own retention,
// M5-30); the request starts a day before the instance's retention, so lines an agent delivers
// late are covered by the next day's request.
func (e *Eraser) EraseBefore(ctx context.Context, org tenant.OrgID, before time.Time) error {
	start := time.Now().Add(-(retention.MaxLogDays + 1) * 24 * time.Hour)
	if err := e.client.Delete(ctx, org, logql.Everything(), start, before); err != nil {
		return fmt.Errorf("erase the old logs of %s: %w", org, err)
	}
	return nil
}
