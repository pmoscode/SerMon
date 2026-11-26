// Package postgres is the PostgreSQL/TimescaleDB adapter: migrations, connection pool and
// tenant-scoped transactions (ADR 0007).
//
// Every data access runs in InTenant: the transaction sets sermon.org_id (SET LOCAL), and the
// row-level security policies of the schema restrict all queries to that organization.
// sermon-server connects as an unprivileged role; Open refuses roles that could bypass RLS.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	"github.com/pmoscode/SerMon/server/migrations"
)

var (
	// ErrPrivilegedRole means the connection role could bypass row-level security.
	ErrPrivilegedRole = errors.New("database role must not be superuser, BYPASSRLS or table owner (row-level security would not apply)")
	// ErrInvalidScope means a tenant transaction was requested without a valid tenant scope.
	ErrInvalidScope = errors.New("tenant transaction requires a valid tenant scope")
)

// Migrate applies all pending schema migrations as the owner role. Concurrent server instances
// are serialized by a PostgreSQL advisory lock.
func Migrate(ctx context.Context, ownerDSN string) error { return migrateTo(ctx, ownerDSN, 0) }

// migrateTo applies the migrations up to version (0: all); tests of a migration use it.
func migrateTo(ctx context.Context, ownerDSN string, version int64) error {
	config, err := pgx.ParseConfig(ownerDSN)
	if err != nil {
		return fmt.Errorf("parse migration DSN: %w", err)
	}
	sqlDB := stdlib.OpenDB(*config)
	defer func() { _ = sqlDB.Close() }()

	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return fmt.Errorf("create migration lock: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS, goose.WithSessionLocker(locker))
	if err != nil {
		return fmt.Errorf("create migration provider: %w", err)
	}
	if version > 0 {
		_, err = provider.UpTo(ctx, version)
	} else {
		_, err = provider.Up(ctx)
	}
	if err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

// DB is the connection pool of the unprivileged application role.
type DB struct {
	pool *pgxpool.Pool
}

// Open connects with appDSN and verifies that the role is subject to row-level security.
func Open(ctx context.Context, appDSN string) (*DB, error) {
	pool, err := pgxpool.New(ctx, appDSN)
	if err != nil {
		return nil, fmt.Errorf("open database pool: %w", err)
	}
	if err := verifyUnprivileged(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return &DB{pool: pool}, nil
}

// PoolSize returns the most connections the pool opens.
func (db *DB) PoolSize() int { return int(db.pool.Config().MaxConns) }

// verifyUnprivileged rejects roles for which RLS does not apply or could be switched off.
func verifyUnprivileged(ctx context.Context, pool *pgxpool.Pool) error {
	var superuser, bypassRLS, ownsTables bool
	err := pool.QueryRow(ctx, `
		SELECT r.rolsuper, r.rolbypassrls,
		       EXISTS (SELECT 1 FROM pg_class c
		               WHERE c.relowner = r.oid AND c.relkind IN ('r', 'p')
		                 AND c.relnamespace = 'public'::regnamespace)
		FROM pg_roles r WHERE r.rolname = current_user`).Scan(&superuser, &bypassRLS, &ownsTables)
	if err != nil {
		return fmt.Errorf("inspect database role: %w", err)
	}
	if superuser || bypassRLS || ownsTables {
		return ErrPrivilegedRole
	}
	return nil
}

// Close closes the connection pool.
func (db *DB) Close() { db.pool.Close() }

// Ping checks the database connection (readiness check).
func (db *DB) Ping(ctx context.Context) error {
	if err := db.pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	return nil
}

// InTenant runs fn in a transaction bound to the organization of scope. The tenant setting is
// transaction-local and cannot leak to other queries on the same connection. The transaction
// commits if fn returns nil and rolls back otherwise.
func (db *DB) InTenant(ctx context.Context, scope tenant.Scope, fn func(q *sqlcdb.Queries) error) error {
	if !scope.Valid() {
		return ErrInvalidScope
	}
	err := pgx.BeginFunc(ctx, db.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT set_config('sermon.org_id', $1, true)", scope.OrgID().String()); err != nil {
			return fmt.Errorf("set tenant: %w", err)
		}
		return fn(sqlcdb.New(tx))
	})
	if err != nil {
		return fmt.Errorf("tenant transaction: %w", err)
	}
	return nil
}

// withoutTenant returns queries outside any tenant transaction: tenant tables are invisible
// (RLS), only instance-level tables and SECURITY DEFINER checks are usable.
func (db *DB) withoutTenant() *sqlcdb.Queries { return sqlcdb.New(db.pool) }

// instanceTx runs fn in a transaction without tenant: for instance-level tables only (no RLS).
func (db *DB) instanceTx(ctx context.Context, fn func(q *sqlcdb.Queries) error) error {
	if err := pgx.BeginFunc(ctx, db.pool, func(tx pgx.Tx) error { return fn(sqlcdb.New(tx)) }); err != nil {
		return fmt.Errorf("instance transaction: %w", err)
	}
	return nil
}

// Listen calls fn with the payload of each notification on channel until ctx ends (M4-11). Its
// connection leaves the pool for good; after an error it reconnects every retry.
func (db *DB) Listen(ctx context.Context, channel string, retry time.Duration, fn func(payload string), onError func(error)) {
	for {
		err := db.listenOnce(ctx, channel, fn)
		if ctx.Err() != nil {
			return
		}
		onError(err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(retry):
		}
	}
}

func (db *DB) listenOnce(ctx context.Context, channel string, fn func(payload string)) error {
	pooled, err := db.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	// A listening connection must not go back to the pool: it would hand notifications to others.
	conn := pooled.Hijack()
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{channel}.Sanitize()); err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return fmt.Errorf("wait for notification: %w", err)
		}
		fn(n.Payload)
	}
}
