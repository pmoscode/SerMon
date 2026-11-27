// Package backup creates and restores the logical backup of an instance (V1-05, ADR 0042):
// master data, events, the agent CA and the configuration kept in the database, without the
// raw metrics, encrypted to the operator's backup keys and bound to the master keys.
package backup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"filippo.io/age"

	"github.com/pmoscode/SerMon/server/internal/platform/backupfile"
	"github.com/pmoscode/SerMon/server/internal/secrets"
)

var (
	// ErrNotEmpty means the target database holds an instance already.
	ErrNotEmpty = errors.New("the database is not empty: restore only into a new database")
	// ErrSchema means the backup's migrations don't fit the database or this server.
	ErrSchema = errors.New("the schema of the backup does not fit")
)

// Info describes the backed-up database.
type Info struct {
	Migrations  []int64
	Postgres    string
	TimescaleDB string
}

// Source is a consistent snapshot of the database.
type Source interface {
	Info(ctx context.Context) (Info, error)
	// Tables returns the tables of the backup in an order that satisfies their foreign keys.
	Tables(ctx context.Context) ([]backupfile.Table, error)
	// CopyOut writes the rows of a table as COPY text and returns their number.
	CopyOut(ctx context.Context, t backupfile.Table, w io.Writer) (int64, error)
	Sequences(ctx context.Context) ([]backupfile.Sequence, error)
}

// Target is the database a backup is restored into.
type Target interface {
	// Prepare refuses a database with an instance (ErrNotEmpty) or other migrations (ErrSchema)
	// and migrates it to exactly the migrations of the backup.
	Prepare(ctx context.Context, migrations []int64) error
	// Load runs fn in one transaction and commits only if it returns nil.
	Load(ctx context.Context, fn func(Loader) error) error
	// Finish applies the migrations of this server newer than the backup's.
	Finish(ctx context.Context) error
}

// Loader writes into the transaction of Target.Load.
type Loader interface {
	// CopyIn loads the COPY text of a table and returns the number of rows.
	CopyIn(ctx context.Context, t backupfile.Table, data io.Reader) (int64, error)
	SetSequences(ctx context.Context, seqs []backupfile.Sequence) error
	// Restored notes the backup for the instance's audit log, written with the commit.
	Restored(h backupfile.Header)
}

// Progress is told each table when it is done.
type Progress func(table string, rows int64)

// Summary counts what a backup or restore carried.
type Summary struct {
	ID     string
	Tables int
	Rows   int64
}

// CreateOptions configure a backup.
type CreateOptions struct {
	Recipients    []age.Recipient
	Keys          *secrets.Box
	ServerVersion string
	Now           time.Time
	Progress      Progress
}

// Create writes the backup of src to dst. Only a nil error means the backup is complete.
func Create(ctx context.Context, src Source, dst io.Writer, o CreateOptions) (Summary, error) {
	id, err := newID()
	if err != nil {
		return Summary{}, err
	}
	info, err := src.Info(ctx)
	if err != nil {
		return Summary{}, fmt.Errorf("backup: database: %w", err)
	}
	tables, err := src.Tables(ctx)
	if err != nil {
		return Summary{}, fmt.Errorf("backup: tables: %w", err)
	}
	w, err := backupfile.NewWriter(dst, o.Recipients, o.Keys, backupfile.Header{
		ID: id, CreatedAt: o.Now.UTC(), ServerVersion: o.ServerVersion,
		Migrations: info.Migrations, Postgres: info.Postgres, TimescaleDB: info.TimescaleDB,
	})
	if err != nil {
		return Summary{}, err //nolint:wrapcheck // backupfile's errors name the backup already
	}
	summary := Summary{ID: id}
	for _, t := range tables {
		if err := w.BeginTable(t); err != nil {
			return Summary{}, err //nolint:wrapcheck // see above
		}
		rows, err := src.CopyOut(ctx, t, w)
		if err != nil {
			return Summary{}, fmt.Errorf("backup: table %s: %w", t.Name, err)
		}
		if err := w.EndTable(rows); err != nil {
			return Summary{}, err //nolint:wrapcheck // see above
		}
		summary.Tables++
		summary.Rows += rows
		if o.Progress != nil {
			o.Progress(t.Name, rows)
		}
	}
	seqs, err := src.Sequences(ctx)
	if err != nil {
		return Summary{}, fmt.Errorf("backup: sequences: %w", err)
	}
	if err := w.Sequences(seqs); err != nil {
		return Summary{}, err //nolint:wrapcheck // see above
	}
	if err := w.Close(); err != nil {
		return Summary{}, err //nolint:wrapcheck // see above
	}
	return summary, nil
}

func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("backup ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// RestoreOptions configure a restore.
type RestoreOptions struct {
	Identities []age.Identity
	Keys       *secrets.Box
	Progress   Progress
}

// Restore loads the backup src into target: the keys are checked before the database is
// touched, the data is committed only after the seal of the file is verified.
func Restore(ctx context.Context, src io.Reader, target Target, o RestoreOptions) (backupfile.Header, Summary, error) {
	r, err := backupfile.Open(src, o.Identities, o.Keys)
	if err != nil {
		return backupfile.Header{}, Summary{}, err //nolint:wrapcheck // backupfile's errors name the backup already
	}
	h := r.Header()
	if err := target.Prepare(ctx, h.Migrations); err != nil {
		return h, Summary{}, fmt.Errorf("restore: %w", err)
	}
	summary := Summary{ID: h.ID}
	err = target.Load(ctx, func(l Loader) error {
		for {
			item, err := r.Next()
			if errors.Is(err, io.EOF) {
				l.Restored(h) // the seal is verified
				return nil
			}
			if err != nil {
				return err //nolint:wrapcheck // see above
			}
			if item.Table == nil {
				if err := l.SetSequences(ctx, item.Sequences); err != nil {
					return fmt.Errorf("sequences: %w", err)
				}
				continue
			}
			loaded, err := l.CopyIn(ctx, *item.Table, item.Data)
			if err != nil {
				return fmt.Errorf("table %s: %w", item.Table.Name, err)
			}
			rows, err := item.Data.Rows()
			if err != nil {
				return err //nolint:wrapcheck // see above
			}
			if loaded != rows {
				return fmt.Errorf("%w: table %s took %d rows, the backup has %d", backupfile.ErrCorrupt, item.Table.Name, loaded, rows)
			}
			summary.Tables++
			summary.Rows += rows
			if o.Progress != nil {
				o.Progress(item.Table.Name, rows)
			}
		}
	})
	if err != nil {
		return h, Summary{}, fmt.Errorf("restore: %w", err)
	}
	if err := target.Finish(ctx); err != nil {
		return h, Summary{}, fmt.Errorf("restore: migrations after the backup: %w", err)
	}
	return h, summary, nil
}
