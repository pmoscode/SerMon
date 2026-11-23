package loki

import (
	"context"
	"fmt"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/organization"
	"github.com/pmoscode/SerMon/server/internal/domain/logql"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Eraser deletes an organization's logs when it is purged (M3-10, D29).
type Eraser struct{ client *Client }

var _ organization.LogEraser = (*Eraser)(nil)

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
