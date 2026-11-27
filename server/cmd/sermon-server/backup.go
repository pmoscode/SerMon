package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/app/backup"
	"github.com/pmoscode/SerMon/server/internal/config"
	"github.com/pmoscode/SerMon/server/internal/platform/backupfile"
)

// exitRefused: the backup or the database was refused – wrong keys, a damaged or changed file,
// a database with an instance or another schema (V1-05). Nothing was changed.
const exitRefused = 3

// stdin is where "restore --in -" reads.
var stdin io.Reader = os.Stdin

// runBackupKeygen writes a new backup key to stdout (V1-05, ADR 0042): an age identity file
// whose comment carries the public key for SERMON_BACKUP_RECIPIENTS.
func runBackupKeygen(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		_, _ = fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	file, recipient, err := backupfile.GenerateIdentity(time.Now())
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "sermon-server: %v\n", err)
		return exitError
	}
	_, _ = io.WriteString(stdout, file)
	_, _ = fmt.Fprintf(stderr, "public key: %s\nPut it into SERMON_BACKUP_RECIPIENTS; keep the secret key away from the server.\n", recipient)
	return exitOK
}

// runBackup writes the encrypted backup of the database (V1-05) to --out: a file (never
// replaced; complete or absent) or - for stdout.
func runBackup(ctx context.Context, args []string, src config.Source, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("backup", flag.ContinueOnError)
	flags.SetOutput(stderr)
	out := flags.String("out", "", "write the backup to this file, or - for stdout")
	if err := flags.Parse(args); err != nil || flags.NArg() > 0 || *out == "" {
		_, _ = fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	cfg, err := config.LoadBackup(src)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "sermon-server: %v\n", err)
		return exitError
	}
	logger := toolLogger(cfg.LogLevel, cfg.LogFormat, stderr)
	dst, err := openOutput(*out, stdout)
	if err != nil {
		logger.ErrorContext(ctx, "backup failed", "error", err)
		return exitError
	}
	started := time.Now()
	summary, err := createBackup(ctx, cfg, dst, logger)
	if err == nil {
		err = dst.commit()
	}
	if err != nil {
		dst.discard()
		logger.ErrorContext(ctx, "backup failed", "error", err)
		return exitError
	}
	logger.InfoContext(ctx, "backup complete – it is useless without the master keys: keep them apart from it",
		"id", summary.ID, "tables", summary.Tables, "rows", summary.Rows, "bytes", dst.written, "to", *out,
		"duration", time.Since(started).Round(time.Millisecond).String())
	return exitOK
}

func createBackup(ctx context.Context, cfg config.Backup, dst io.Writer, logger *slog.Logger) (backup.Summary, error) {
	source, err := postgres.OpenBackupSource(ctx, cfg.DatabaseURL)
	if err != nil {
		return backup.Summary{}, fmt.Errorf("database: %w", err)
	}
	defer source.Close(ctx)
	summary, err := backup.Create(ctx, source, dst, backup.CreateOptions{
		Recipients: cfg.Recipients, Keys: cfg.MasterKeys, ServerVersion: version, Now: time.Now(),
		Progress: func(table string, rows int64) {
			logger.DebugContext(ctx, "table backed up", "table", table, "rows", rows)
		},
	})
	if err != nil {
		return backup.Summary{}, fmt.Errorf("create: %w", err)
	}
	return summary, nil
}

// output is the destination of a backup; a file appears under its name only when complete.
type output struct {
	w       io.Writer
	file    *os.File
	path    string
	written int64
}

func openOutput(path string, stdout io.Writer) (*output, error) {
	if path == "-" {
		return &output{w: stdout}, nil
	}
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s exists – a backup is never replaced", path)
	}
	f, err := os.OpenFile(path+".partial", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: the operator's own path
	if err != nil {
		return nil, fmt.Errorf("create the backup file: %w", err)
	}
	return &output{w: f, file: f, path: path}, nil
}

func (o *output) Write(p []byte) (int, error) {
	n, err := o.w.Write(p)
	o.written += int64(n)
	return n, err //nolint:wrapcheck // the writer's own error
}

// commit makes a file backup visible under its name (never replacing a file).
func (o *output) commit() error {
	if o.file == nil {
		return nil
	}
	if err := o.file.Sync(); err != nil {
		return fmt.Errorf("write the backup file: %w", err)
	}
	if err := o.file.Close(); err != nil {
		return fmt.Errorf("write the backup file: %w", err)
	}
	if err := os.Link(o.path+".partial", o.path); err != nil {
		return fmt.Errorf("name the backup file: %w", err)
	}
	return os.Remove(o.path + ".partial") //nolint:wrapcheck // only the leftover name
}

func (o *output) discard() {
	if o.file != nil {
		_ = o.file.Close()
		_ = os.Remove(o.path + ".partial")
	}
}

// runRestore loads a backup into a new database (V1-05) as the owner of the schema; with
// --install-master-keys it also writes the given master keys where the server reads them.
func runRestore(ctx context.Context, args []string, src config.Source, stderr io.Writer) int {
	flags := flag.NewFlagSet("restore", flag.ContinueOnError)
	flags.SetOutput(stderr)
	in := flags.String("in", "", "read the backup from this file, or - for stdin")
	install := flags.String("install-master-keys", "", "write the master keys to this file after the restore (kept if it holds the same keys)")
	if err := flags.Parse(args); err != nil || flags.NArg() > 0 || *in == "" {
		_, _ = fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	cfg, err := config.LoadRestore(src)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "sermon-server: %v\n", err)
		return exitError
	}
	logger := toolLogger(cfg.LogLevel, cfg.LogFormat, stderr)
	fail := func(err error) int {
		logger.ErrorContext(ctx, "restore failed – the database is unchanged", "error", err)
		if refused(err) {
			return exitRefused
		}
		return exitError
	}
	if *install != "" {
		if err := checkKeyFile(*install, cfg.MasterKeysText); err != nil {
			return fail(err)
		}
	}
	r := stdin
	if *in != "-" {
		f, err := os.Open(*in)
		if err != nil {
			return fail(fmt.Errorf("open the backup: %w", err))
		}
		defer func() { _ = f.Close() }()
		r = f
	}
	started := time.Now()
	h, summary, err := backup.Restore(ctx, r, postgres.NewRestoreTarget(cfg.DatabaseMigrateURL), backup.RestoreOptions{
		Identities: cfg.Identities, Keys: cfg.MasterKeys,
		Progress: func(table string, rows int64) {
			logger.DebugContext(ctx, "table restored", "table", table, "rows", rows)
		},
	})
	if err != nil {
		return fail(err)
	}
	if *install != "" {
		if err := installKeyFile(*install, cfg.MasterKeysText); err != nil {
			logger.ErrorContext(ctx, "the database is restored, but the master keys could not be written – put them there by hand", "path", *install, "error", err)
			return exitError
		}
	}
	logger.InfoContext(ctx, "restore complete – start the server; everyone signs in again, the agents reconnect with their certificates",
		"id", h.ID, "created", h.CreatedAt.Format(time.RFC3339), "from_version", h.ServerVersion,
		"tables", summary.Tables, "rows", summary.Rows, "duration", time.Since(started).Round(time.Millisecond).String())
	return exitOK
}

func refused(err error) bool {
	for _, target := range []error{backupfile.ErrIdentity, backupfile.ErrMasterKeys, backupfile.ErrCorrupt, backup.ErrNotEmpty, backup.ErrSchema, errOtherKeyFile} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

var errOtherKeyFile = errors.New("another master key file exists there – it would lock the restored instance out of its secrets")

// checkKeyFile refuses a key file at path with other keys.
func checkKeyFile(path, keys string) error {
	content, err := os.ReadFile(path) //nolint:gosec // G304: the operator's own path
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("read %s: %w", path, err)
	case strings.TrimSpace(string(content)) != strings.TrimSpace(keys):
		return fmt.Errorf("%s: %w", path, errOtherKeyFile)
	}
	return nil
}

// installKeyFile writes the master keys to path unless the same keys are there already.
func installKeyFile(path, keys string) error {
	if err := checkKeyFile(path, keys); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: see above
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if _, err := f.WriteString(strings.TrimSpace(keys) + "\n"); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
