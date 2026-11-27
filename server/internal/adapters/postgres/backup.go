package postgres

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/app/backup"
	"github.com/pmoscode/SerMon/server/internal/platform/backupfile"
	"github.com/pmoscode/SerMon/server/migrations"
)

// backupExcluded are the tables the logical backup leaves out (V1-05, ADR 0042), with the reason.
// Every other table of the schema is in it: a new table is backed up unless it is listed here.
var backupExcluded = map[string]string{
	"goose_db_version":    "the restore applies the backup's migrations itself",
	"metrics_raw":         "raw metrics (with their continuous aggregates): the full backup with pgBackRest keeps them",
	"anomaly_points":      "time series of the anomaly scores, derived from the metrics",
	"log_counts_1m":       "counts of the log lines in Loki, which the backup doesn't hold either (V1-24)",
	"log_counts_1h":       "counts of the log lines in Loki, which the backup doesn't hold either (V1-24)",
	"log_count_marks":     "the counted batches of the log counts, which the backup leaves out",
	"sessions":            "sign-ins: everyone signs in again after a restore",
	"oidc_flows":          "sign-ins at the OIDC provider in progress (minutes)",
	"webauthn_ceremonies": "passkey ceremonies in progress (minutes)",
	"alert_leases":        "leases of the server instances",
	"update_leases":       "leases of the server instances",
	"anomaly_leases":      "leases of the server instances",
}

// BackupExclusions returns the tables the backup leaves out with the reason.
func BackupExclusions() map[string]string { return maps.Clone(backupExcluded) }

// sessionSettings make COPY text independent of the server's and the client's defaults, in the
// backup as in the restore (as pg_dump does).
var sessionSettings = []string{
	"SET LOCAL TimeZone = 'UTC'",
	"SET LOCAL DateStyle = 'ISO, YMD'",
	"SET LOCAL IntervalStyle = 'postgres'",
	"SET LOCAL extra_float_digits = 3",
	"SET LOCAL bytea_output = 'hex'",
	"SET LOCAL statement_timeout = 0",
	"SET LOCAL lock_timeout = 0",
	"SET LOCAL idle_in_transaction_session_timeout = 0",
	// A role that row-level security still applies to gets an error instead of a backup or a
	// restore without the organizations' rows.
	"SET LOCAL row_security = off",
}

// BackupSource is a consistent snapshot of the database (one REPEATABLE READ transaction) for
// the role sermon_backup: read-only, BYPASSRLS.
type BackupSource struct {
	conn *pgx.Conn
	tx   pgx.Tx
}

var _ backup.Source = (*BackupSource)(nil)

// OpenBackupSource connects with dsn and starts the snapshot.
func OpenBackupSource(ctx context.Context, dsn string) (*BackupSource, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err == nil {
		err = settings(ctx, tx)
	}
	if err != nil {
		_ = conn.Close(ctx)
		return nil, fmt.Errorf("start snapshot: %w", err)
	}
	return &BackupSource{conn: conn, tx: tx}, nil
}

func settings(ctx context.Context, tx pgx.Tx) error {
	for _, s := range sessionSettings {
		if _, err := tx.Exec(ctx, s); err != nil {
			return fmt.Errorf("%s: %w", s, err)
		}
	}
	return nil
}

// Close ends the snapshot.
func (s *BackupSource) Close(ctx context.Context) {
	_ = s.tx.Rollback(ctx)
	_ = s.conn.Close(ctx)
}

// Info returns the applied migrations and the versions of PostgreSQL and TimescaleDB.
func (s *BackupSource) Info(ctx context.Context) (backup.Info, error) {
	var info backup.Info
	var err error
	if info.Migrations, err = applied(ctx, s.tx); err != nil {
		return backup.Info{}, err
	}
	if err := s.tx.QueryRow(ctx, `SELECT current_setting('server_version'),
		coalesce((SELECT extversion FROM pg_extension WHERE extname = 'timescaledb'), '')`).Scan(&info.Postgres, &info.TimescaleDB); err != nil {
		return backup.Info{}, fmt.Errorf("versions: %w", err)
	}
	return info, nil
}

// querier is a connection or a transaction.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// applied returns the migrations applied in the database (goose keeps one row per change).
func applied(ctx context.Context, q querier) ([]int64, error) {
	var exists bool
	if err := q.QueryRow(ctx, "SELECT to_regclass('public.goose_db_version') IS NOT NULL").Scan(&exists); err != nil {
		return nil, fmt.Errorf("migrations: %w", err)
	}
	if !exists {
		return nil, nil
	}
	rows, err := q.Query(ctx, `SELECT version_id FROM (
		SELECT DISTINCT ON (version_id) version_id, is_applied FROM public.goose_db_version ORDER BY version_id, id DESC
	) v WHERE is_applied AND version_id > 0 ORDER BY version_id`)
	if err != nil {
		return nil, fmt.Errorf("migrations: %w", err)
	}
	versions, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, fmt.Errorf("migrations: %w", err)
	}
	return versions, nil
}

// table is a table of the schema with its columns.
type table struct {
	oid     uint32
	name    string
	columns []string
}

// backupTables returns the tables of the backup, ordered by their foreign keys: every table after
// the tables it references. A table that references an excluded one is an error (the restore
// could not load it).
func backupTables(ctx context.Context, q querier) ([]table, error) {
	rows, err := q.Query(ctx, `SELECT c.oid, c.relname,
		array(SELECT a.attname::text FROM pg_attribute a
		      WHERE a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped AND a.attgenerated = ''
		      ORDER BY a.attnum)
		FROM pg_class c
		WHERE c.relnamespace = 'public'::regnamespace AND c.relkind IN ('r', 'p') AND NOT c.relispartition
		ORDER BY c.relname`)
	if err != nil {
		return nil, fmt.Errorf("tables: %w", err)
	}
	all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (table, error) {
		var t table
		err := r.Scan(&t.oid, &t.name, &t.columns)
		return t, err //nolint:wrapcheck // wrapped below
	})
	if err != nil {
		return nil, fmt.Errorf("tables: %w", err)
	}
	byOID := map[uint32]string{}
	var included []table
	for _, t := range all {
		byOID[t.oid] = t.name
		if _, excluded := backupExcluded[t.name]; !excluded {
			included = append(included, t)
		}
	}
	rows, err = q.Query(ctx, `SELECT conrelid, confrelid FROM pg_constraint
		WHERE contype = 'f' AND conparentid = 0 AND connamespace = 'public'::regnamespace`)
	if err != nil {
		return nil, fmt.Errorf("foreign keys: %w", err)
	}
	references := map[string][]string{}
	err = forEachRow(rows, func(r pgx.Rows) error {
		var from, to uint32
		if err := r.Scan(&from, &to); err != nil {
			return err //nolint:wrapcheck // wrapped below
		}
		if f, t := byOID[from], byOID[to]; f != "" && t != "" && f != t {
			references[f] = append(references[f], t)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("foreign keys: %w", err)
	}
	return orderByReferences(included, references)
}

func forEachRow(rows pgx.Rows, fn func(pgx.Rows) error) error {
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err() //nolint:wrapcheck // the callers wrap
}

// orderByReferences sorts tables so that each comes after the tables it references (by name
// among equals, so the order is stable).
func orderByReferences(tables []table, references map[string][]string) ([]table, error) {
	byName := map[string]table{}
	for _, t := range tables {
		byName[t.name] = t
	}
	waiting := map[string]int{}
	referencedBy := map[string][]string{}
	for _, t := range tables {
		for _, ref := range references[t.name] {
			if _, ok := byName[ref]; !ok {
				return nil, fmt.Errorf("table %s references %s, which the backup leaves out", t.name, ref)
			}
			waiting[t.name]++
			referencedBy[ref] = append(referencedBy[ref], t.name)
		}
	}
	var ready, ordered []string
	for _, t := range tables {
		if waiting[t.name] == 0 {
			ready = append(ready, t.name)
		}
	}
	for len(ready) > 0 {
		slices.Sort(ready)
		next := ready[0]
		ready = ready[1:]
		ordered = append(ordered, next)
		for _, dependent := range referencedBy[next] {
			if waiting[dependent]--; waiting[dependent] == 0 {
				ready = append(ready, dependent)
			}
		}
	}
	if len(ordered) != len(tables) {
		return nil, errors.New("the foreign keys of the tables form a cycle")
	}
	out := make([]table, len(ordered))
	for i, name := range ordered {
		out[i] = byName[name]
	}
	return out, nil
}

// Tables returns the tables of the backup in the order of their foreign keys.
func (s *BackupSource) Tables(ctx context.Context) ([]backupfile.Table, error) {
	tables, err := backupTables(ctx, s.tx)
	if err != nil {
		return nil, err
	}
	out := make([]backupfile.Table, len(tables))
	for i, t := range tables {
		out[i] = backupfile.Table{Name: t.name, Columns: t.columns}
	}
	return out, nil
}

func columnList(columns []string) string {
	quoted := make([]string, len(columns))
	for i, c := range columns {
		quoted[i] = pgx.Identifier{c}.Sanitize()
	}
	return strings.Join(quoted, ", ")
}

// CopyOut writes the rows of t as COPY text; for partitioned tables those of every partition.
func (s *BackupSource) CopyOut(ctx context.Context, t backupfile.Table, w io.Writer) (int64, error) {
	sql := "COPY (SELECT " + columnList(t.Columns) + " FROM " + pgx.Identifier{"public", t.Name}.Sanitize() + ") TO STDOUT"
	tag, err := s.conn.PgConn().CopyTo(ctx, w, sql)
	if err != nil {
		return 0, fmt.Errorf("copy %s: %w", t.Name, err)
	}
	return tag.RowsAffected(), nil
}

// Sequences returns the state of every sequence of the schema.
func (s *BackupSource) Sequences(ctx context.Context) ([]backupfile.Sequence, error) {
	rows, err := s.tx.Query(ctx, `SELECT relname FROM pg_class WHERE relnamespace = 'public'::regnamespace AND relkind = 'S' ORDER BY relname`)
	if err != nil {
		return nil, fmt.Errorf("sequences: %w", err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("sequences: %w", err)
	}
	seqs := make([]backupfile.Sequence, 0, len(names))
	for _, name := range names {
		seq := backupfile.Sequence{Name: name}
		if err := s.tx.QueryRow(ctx, "SELECT last_value, is_called FROM "+pgx.Identifier{"public", name}.Sanitize()).Scan(&seq.Value, &seq.Called); err != nil {
			return nil, fmt.Errorf("sequence %s: %w", name, err)
		}
		seqs = append(seqs, seq)
	}
	return seqs, nil
}

// MigrationVersions returns the versions of the migrations this server carries.
func MigrationVersions() ([]int64, error) {
	files, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		return nil, fmt.Errorf("migrations: %w", err)
	}
	versions := make([]int64, 0, len(files))
	for _, f := range files {
		digits, _, _ := strings.Cut(f, "_")
		v, err := strconv.ParseInt(digits, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("migration %s: %w", f, err)
		}
		versions = append(versions, v)
	}
	slices.Sort(versions)
	return versions, nil
}

// RestoreTarget restores into a new database as the owner of the schema (the role of the
// migrations, ADR 0007).
type RestoreTarget struct {
	dsn string
}

var _ backup.Target = (*RestoreTarget)(nil)

// NewRestoreTarget returns the target behind the owner's dsn.
func NewRestoreTarget(ownerDSN string) *RestoreTarget { return &RestoreTarget{dsn: ownerDSN} }

// Prepare checks the database and migrates it to the backup's migrations.
func (r *RestoreTarget) Prepare(ctx context.Context, want []int64) error {
	known, err := MigrationVersions()
	if err != nil {
		return err
	}
	for _, v := range want {
		if !slices.Contains(known, v) {
			return fmt.Errorf("%w: the backup has migration %d this server lacks – restore with the same or a newer SerMon", backup.ErrSchema, v)
		}
	}
	conn, err := pgx.Connect(ctx, r.dsn)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	have, err := applied(ctx, conn)
	if err != nil {
		return err
	}
	for _, v := range have {
		if !slices.Contains(want, v) {
			return fmt.Errorf("%w: the database has migration %d the backup lacks – restore into a new database", backup.ErrSchema, v)
		}
	}
	if len(have) > 0 {
		if err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error { return refuseInstance(ctx, tx) }); err != nil {
			return fmt.Errorf("check the database: %w", err)
		}
	}
	if err := migrateTo(ctx, r.dsn, slices.Max(want)); err != nil {
		return err
	}
	if have, err = applied(ctx, conn); err != nil {
		return err
	}
	if !slices.Equal(have, sortedCopy(want)) {
		return fmt.Errorf("%w: the database has migrations %v after migrating, the backup %v", backup.ErrSchema, have, want)
	}
	return nil
}

func sortedCopy(v []int64) []int64 {
	c := slices.Clone(v)
	slices.Sort(c)
	return slices.Compact(c)
}

// refuseInstance returns ErrNotEmpty if the database holds an instance: an organization or a
// user. FORCE ROW LEVEL SECURITY hides their rows from the owner as well, so it is lifted in tx.
func refuseInstance(ctx context.Context, tx pgx.Tx) error {
	for _, t := range []string{"users", "organizations"} {
		if _, err := tx.Exec(ctx, "ALTER TABLE public."+t+" NO FORCE ROW LEVEL SECURITY"); err != nil {
			return fmt.Errorf("check for an instance: %w", err)
		}
	}
	var found bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM public.users) OR EXISTS (SELECT 1 FROM public.organizations)").Scan(&found); err != nil {
		return fmt.Errorf("check for an instance: %w", err)
	}
	if found {
		return backup.ErrNotEmpty
	}
	return nil
}

// Load runs fn in one transaction: FORCE ROW LEVEL SECURITY lifted and the triggers of the tables
// off (the owner loads every organization; the audit log keeps its hash chain), both set again
// before the commit. The lock of every table keeps the server out meanwhile.
func (r *RestoreTarget) Load(ctx context.Context, fn func(backup.Loader) error) error {
	conn, err := pgx.Connect(ctx, r.dsn)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error { //nolint:wrapcheck // the steps wrap
		if err := settings(ctx, tx); err != nil {
			return err
		}
		tables, err := backupTables(ctx, tx)
		if err != nil {
			return err
		}
		undo, err := prepareLoad(ctx, tx, tables)
		if err != nil {
			return err
		}
		loader := &loader{tx: tx, tables: map[string]table{}}
		for _, t := range tables {
			loader.tables[t.name] = t
		}
		if err := fn(loader); err != nil {
			return err
		}
		for _, stmt := range undo {
			if _, err := tx.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("%s: %w", stmt, err)
			}
		}
		return loader.audit(ctx)
	})
}

// audit writes the restore into the instance's audit log (its trigger is on again).
func (l *loader) audit(ctx context.Context) error {
	if l.restored == nil {
		return nil
	}
	h := l.restored
	details, err := json.Marshal(map[string]any{
		"created_at": h.CreatedAt, "server_version": h.ServerVersion, "migrations": len(h.Migrations),
	})
	if err != nil {
		return fmt.Errorf("audit the restore: %w", err)
	}
	if _, err := l.tx.Exec(ctx, `INSERT INTO public.instance_audit_log (action, target, details) VALUES ('instance.restored', $1, $2)`,
		"backup:"+h.ID, details); err != nil {
		return fmt.Errorf("audit the restore: %w", err)
	}
	return nil
}

// prepareLoad lifts FORCE ROW LEVEL SECURITY on the tables and their partitions, refuses an
// instance, empties the tables and disables their triggers; it returns the statements that undo
// all but the emptying.
func prepareLoad(ctx context.Context, tx pgx.Tx, tables []table) ([]string, error) {
	oids := make([]uint32, len(tables))
	for i, t := range tables {
		oids[i] = t.oid
	}
	rows, err := tx.Query(ctx, `SELECT c.relname FROM pg_class c
		WHERE c.relforcerowsecurity AND (c.oid = ANY ($1::oid[])
		   OR c.oid IN (SELECT relid FROM unnest($1::oid[]) o, pg_partition_tree(o) WHERE relid IS NOT NULL))
		ORDER BY c.relname`, oids)
	if err != nil {
		return nil, fmt.Errorf("tables with FORCE ROW LEVEL SECURITY: %w", err)
	}
	forced, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("tables with FORCE ROW LEVEL SECURITY: %w", err)
	}
	var undo []string
	for _, name := range forced {
		id := pgx.Identifier{"public", name}.Sanitize()
		if _, err := tx.Exec(ctx, "ALTER TABLE "+id+" NO FORCE ROW LEVEL SECURITY"); err != nil {
			return nil, fmt.Errorf("lift FORCE ROW LEVEL SECURITY on %s: %w", name, err)
		}
		undo = append(undo, "ALTER TABLE "+id+" FORCE ROW LEVEL SECURITY")
	}
	if err := refuseInstance(ctx, tx); err != nil {
		return nil, err
	}
	names := make([]string, len(tables))
	for i, t := range tables {
		names[i] = pgx.Identifier{"public", t.name}.Sanitize()
	}
	// CASCADE reaches the excluded tables that reference these (sessions, metrics): in a
	// database without users and organizations they hold nothing of an instance.
	if _, err := tx.Exec(ctx, "TRUNCATE "+strings.Join(names, ", ")+" CASCADE"); err != nil {
		return nil, fmt.Errorf("empty the tables: %w", err)
	}
	rows, err = tx.Query(ctx, `SELECT c.relname, t.tgname FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid
		WHERE NOT t.tgisinternal AND t.tgenabled <> 'D' AND t.tgparentid = 0 AND t.tgrelid = ANY ($1::oid[])
		ORDER BY 1, 2`, oids)
	if err != nil {
		return nil, fmt.Errorf("triggers: %w", err)
	}
	type trigger struct{ table, name string }
	triggers, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (trigger, error) {
		var tr trigger
		err := r.Scan(&tr.table, &tr.name)
		return tr, err //nolint:wrapcheck // wrapped below
	})
	if err != nil {
		return nil, fmt.Errorf("triggers: %w", err)
	}
	for _, tr := range triggers {
		on := "ALTER TABLE " + pgx.Identifier{"public", tr.table}.Sanitize()
		if _, err := tx.Exec(ctx, on+" DISABLE TRIGGER "+pgx.Identifier{tr.name}.Sanitize()); err != nil {
			return nil, fmt.Errorf("disable trigger %s: %w", tr.name, err)
		}
		undo = append(undo, on+" ENABLE TRIGGER "+pgx.Identifier{tr.name}.Sanitize())
	}
	return undo, nil
}

// loader writes into the restore transaction; it takes only tables and sequences of this schema.
type loader struct {
	tx       pgx.Tx
	tables   map[string]table
	restored *backupfile.Header
}

// Restored notes the backup for the audit log.
func (l *loader) Restored(h backupfile.Header) { l.restored = &h }

// CopyIn loads the COPY text of t; its columns must be those of the table.
func (l *loader) CopyIn(ctx context.Context, t backupfile.Table, data io.Reader) (int64, error) {
	have, ok := l.tables[t.Name]
	if !ok {
		return 0, fmt.Errorf("%w: table %s is not in this schema", backup.ErrSchema, t.Name)
	}
	if !slices.Equal(sortedStrings(have.columns), sortedStrings(t.Columns)) {
		return 0, fmt.Errorf("%w: the columns of %s differ (backup %v, database %v)", backup.ErrSchema, t.Name, t.Columns, have.columns)
	}
	sql := "COPY " + pgx.Identifier{"public", t.Name}.Sanitize() + " (" + columnList(t.Columns) + ") FROM STDIN"
	tag, err := l.tx.Conn().PgConn().CopyFrom(ctx, data, sql)
	if err != nil {
		return 0, fmt.Errorf("copy: %w", err)
	}
	return tag.RowsAffected(), nil
}

func sortedStrings(v []string) []string {
	c := slices.Clone(v)
	slices.SortFunc(c, cmp.Compare[string])
	return c
}

// SetSequences sets the sequences of the schema.
func (l *loader) SetSequences(ctx context.Context, seqs []backupfile.Sequence) error {
	for _, s := range seqs {
		var exists bool
		if err := l.tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_class WHERE relnamespace = 'public'::regnamespace AND relkind = 'S' AND relname = $1)`, s.Name).Scan(&exists); err != nil {
			return fmt.Errorf("sequence %s: %w", s.Name, err)
		}
		if !exists {
			return fmt.Errorf("%w: sequence %s is not in this schema", backup.ErrSchema, s.Name)
		}
		if _, err := l.tx.Exec(ctx, "SELECT setval($1::regclass, $2, $3)", pgx.Identifier{"public", s.Name}.Sanitize(), s.Value, s.Called); err != nil {
			return fmt.Errorf("sequence %s: %w", s.Name, err)
		}
	}
	return nil
}

// Finish applies the migrations of this server newer than the backup's.
func (r *RestoreTarget) Finish(ctx context.Context) error { return Migrate(ctx, r.dsn) }
