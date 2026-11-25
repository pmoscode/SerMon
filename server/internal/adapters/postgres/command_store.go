package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/updates"
	"github.com/pmoscode/SerMon/server/internal/domain/command"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	"github.com/pmoscode/SerMon/server/internal/domain/textsafe"
)

// CommandsChannel is the NOTIFY channel that wakes the instance holding a node's session; the
// payload is the node's ID.
const CommandsChannel = "sermon_node_commands"

// Limits of the refreshes on request (M4-11).
const (
	RefreshEvery     = 5 * time.Minute
	OrgRefreshes     = 60 // per minute
	refreshOrgWindow = time.Minute
)

// ErrTooSoon means a refresh was asked for the node or too many for the organization a moment ago.
var ErrTooSoon = updates.ErrTooSoon

// CommandStore keeps the commands to the agents (M4-11).
type CommandStore struct{ db *DB }

// NewCommandStore returns the store on db.
func NewCommandStore(db *DB) *CommandStore { return &CommandStore{db: db} }

func orgScope(org tenant.OrgID) (tenant.Scope, error) {
	scope, err := tenant.ForAllGroups(org)
	if err != nil {
		return tenant.Scope{}, fmt.Errorf("commands: %w", err)
	}
	return scope, nil
}

func insertCommand(ctx context.Context, q *sqlcdb.Queries, c command.Command) error {
	if err := c.Validate(); err != nil {
		return err
	}
	var scope pgtype.Int2
	if c.Kind == command.Update {
		scope = pgtype.Int2{Int16: int16(c.Scope), Valid: true}
	}
	if err := q.InsertNodeCommand(ctx, sqlcdb.InsertNodeCommandParams{
		ID: c.ID, NodeID: uuid.UUID(c.Node), Kind: int16(c.Kind), Scope: scope, Reason: c.Reason, Version: c.Version,
		CreatedAt: c.CreatedAt, ExpiresAt: c.ExpiresAt, CreatedBy: optionalUser(c.CreatedBy),
	}); err != nil {
		return wrap(err, "store command")
	}
	return wrap(q.NotifyNodeCommands(ctx, uuid.UUID(c.Node)), "notify command")
}

// Enqueue stores a command for its node and wakes the instance with the node's session once the
// transaction commits. The node must belong to org.
func (s *CommandStore) Enqueue(ctx context.Context, org tenant.OrgID, c command.Command) error {
	scope, err := orgScope(org)
	if err != nil {
		return err
	}
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error { return insertCommand(ctx, q, c) })
}

// RequestRefresh enqueues a refresh of the node's update information unless one was asked for the
// node within RefreshEvery or OrgRefreshes for the organization within a minute (ErrTooSoon).
func (s *CommandStore) RequestRefresh(ctx context.Context, org tenant.OrgID, node identity.NodeID, by identity.UserID, now time.Time) error {
	scope, err := orgScope(org)
	if err != nil {
		return err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("run id: %w", err)
	}
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		recent, err := q.CountRecentRefreshes(ctx, sqlcdb.CountRecentRefreshesParams{
			NodeID: uuid.UUID(node), NodeSince: now.Add(-RefreshEvery), OrgSince: now.Add(-refreshOrgWindow),
		})
		if err != nil {
			return wrap(err, "count refreshes")
		}
		if recent.Node > 0 || recent.Org >= OrgRefreshes {
			return ErrTooSoon
		}
		return insertCommand(ctx, q, command.Command{
			ID: id, Node: node, Kind: command.Refresh, CreatedAt: now, ExpiresAt: now.Add(command.DefaultExpiry), CreatedBy: by,
		})
	})
}

// Deliverable marks the node's expired commands and returns what its session sends: the pending
// ones, with resend (a new session) the sent ones without result too.
func (s *CommandStore) Deliverable(ctx context.Context, org tenant.OrgID, node identity.NodeID, resend bool, now time.Time) ([]command.Command, error) {
	scope, err := orgScope(org)
	if err != nil {
		return nil, err
	}
	var out []command.Command
	err = s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		if err := q.ExpireNodeCommands(ctx, sqlcdb.ExpireNodeCommandsParams{NodeID: uuid.UUID(node), Now: now}); err != nil {
			return wrap(err, "expire commands")
		}
		rows, err := q.DeliverableNodeCommands(ctx, sqlcdb.DeliverableNodeCommandsParams{NodeID: uuid.UUID(node), Resend: resend, Now: now})
		if err != nil {
			return wrap(err, "read commands")
		}
		out = make([]command.Command, 0, len(rows))
		for _, r := range rows {
			out = append(out, command.Command{
				ID: r.ID, Node: node, Kind: command.Kind(r.Kind), Scope: command.Scope(r.Scope.Int16), //nolint:gosec // checked by the table
				Reason: r.Reason, Version: r.Version, State: command.State(r.State), //nolint:gosec // checked by the table
				CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, CreatedBy: userOf(r.CreatedBy), SentAt: r.SentAt,
			})
		}
		return nil
	})
	return out, err
}

// MarkSent records that the node's session sent the commands.
func (s *CommandStore) MarkSent(ctx context.Context, org tenant.OrgID, node identity.NodeID, ids []uuid.UUID, now time.Time) error {
	scope, err := orgScope(org)
	if err != nil {
		return err
	}
	return s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		return wrap(q.MarkNodeCommandsSent(ctx, sqlcdb.MarkNodeCommandsSentParams{NodeID: uuid.UUID(node), Ids: ids, Now: &now}), "mark commands sent")
	})
}

// Record stores the agent's phase of a run of node: false for an unknown run, a run of another
// node, or a phase that comes after a final one (a resend).
func (s *CommandStore) Record(ctx context.Context, org tenant.OrgID, node identity.NodeID, run uuid.UUID, r command.Result) (bool, error) {
	scope, err := orgScope(org)
	if err != nil {
		return false, err
	}
	var n int64
	err = s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		params := sqlcdb.RecordNodeCommandResultParams{
			Phase: pgtype.Int2{Int16: int16(r.Phase), Valid: true}, ResultReason: bounded(textsafe.Visible(r.Reason), command.MaxResult),
			Output: output(r.Output), OutputTruncated: r.OutputTruncated, StartedAt: r.StartedAt, EndedAt: r.EndedAt,
			NodeID: uuid.UUID(node), ID: run,
		}
		if r.Phase.Final() && r.Phase != command.Lost {
			params.ExitCode = pgtype.Int4{Int32: r.ExitCode, Valid: true}
		}
		rows, recordErr := q.RecordNodeCommandResult(ctx, params)
		n = rows
		return wrap(recordErr, "record command result")
	})
	return n == 1, err
}

// Get returns a command of the organization; false if there is none.
func (s *CommandStore) Get(ctx context.Context, org tenant.OrgID, id uuid.UUID) (command.Command, bool, error) {
	scope, err := orgScope(org)
	if err != nil {
		return command.Command{}, false, err
	}
	var out command.Command
	found := false
	err = s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error {
		r, err := q.GetNodeCommand(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return wrap(err, "read command")
		}
		found = true
		out = command.Command{
			ID: r.ID, Node: identity.NodeID(r.NodeID), Kind: command.Kind(r.Kind), Scope: command.Scope(r.Scope.Int16), //nolint:gosec // checked by the table
			Reason: r.Reason, Version: r.Version, State: command.State(r.State), //nolint:gosec // checked by the table
			CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, CreatedBy: userOf(r.CreatedBy), SentAt: r.SentAt,
		}
		if r.Phase.Valid {
			out.Result = &command.Result{
				Phase: command.Phase(r.Phase.Int16), ExitCode: r.ExitCode.Int32, Reason: r.ResultReason, Output: r.Output, //nolint:gosec // 1 to 6
				OutputTruncated: r.OutputTruncated, StartedAt: r.StartedAt, EndedAt: r.EndedAt,
			}
		}
		return nil
	})
	return out, found, err
}

// bounded cuts s to limit bytes at a character boundary.
func bounded(s string, limit int) string {
	s = strings.ToValidUTF8(s, string(utf8.RuneError))
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// output keeps the end of a run's output: PostgreSQL text holds no NUL, and the UI marks the
// other control characters (M4-15).
func output(s string) string {
	s = strings.ReplaceAll(strings.ToValidUTF8(s, string(utf8.RuneError)), "\x00", string(utf8.RuneError))
	if len(s) <= command.MaxOutput {
		return s
	}
	start := len(s) - command.MaxOutput
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}
