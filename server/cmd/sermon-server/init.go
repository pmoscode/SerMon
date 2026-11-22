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
	"time"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/agentca"
	"github.com/pmoscode/SerMon/server/internal/app/setup"
	"github.com/pmoscode/SerMon/server/internal/config"
	"github.com/pmoscode/SerMon/server/internal/secrets"
)

const envMasterKeys = "SERMON_MASTER_KEYS"

// runInit prepares a new installation (KONZEPT §16): master key file, database schema, agent CA
// and a setup token for the first superadmin. Repeating it is safe until the setup is completed
// (existing keys and CA are kept, the token is replaced); afterwards it refuses and changes nothing.
func runInit(ctx context.Context, args []string, src config.Source, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	keysFile := flags.String("master-keys-file", "", "create (if missing) and use this master key file")
	if err := flags.Parse(args); err != nil || flags.NArg() > 0 {
		return exitUsage
	}

	if *keysFile != "" {
		created, err := ensureKeyFile(*keysFile)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "sermon-server: %v\n", err)
			return exitError
		}
		if created {
			_, _ = fmt.Fprintf(stdout, "Created master key file %s – back it up: without it encrypted data is lost.\n", *keysFile)
		}
		src = withMasterKeysFile(src, *keysFile)
	}

	cfg, err := config.Load(src)
	if err == nil && cfg.DatabaseURL == "" {
		err = errors.New("init needs the database: set SERMON_DATABASE_URL and SERMON_DATABASE_MIGRATE_URL")
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "sermon-server: %v\n", err)
		return exitError
	}
	logger := newLogger(cfg, stderr)
	token, expires, err := initialize(ctx, cfg, logger)
	if errors.Is(err, setup.ErrAlreadyInitialized) {
		_, _ = fmt.Fprintln(stderr, "sermon-server: the instance is already initialized – nothing changed")
		return exitError
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "sermon-server: %v\n", err)
		return exitError
	}
	_, _ = fmt.Fprintf(stdout, "Setup token (valid until %s):\n\n    %s\n\nOpen the web UI and enter it to create the first superadmin.\n",
		expires.Format(time.RFC3339), token)
	return exitOK
}

// runMigrate applies the migrations and exits (M2-36): the one-shot service of the production
// compose holds the owner's credentials, the long-running server only those of sermon_app.
func runMigrate(ctx context.Context, src config.Source, stdout, stderr io.Writer) int {
	cfg, err := config.Load(src)
	if err == nil && cfg.DatabaseMigrateURL == "" {
		err = errors.New("migrate needs SERMON_DATABASE_MIGRATE_URL (the owner of the schema)")
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "sermon-server: %v\n", err)
		return exitError
	}
	logger := newLogger(cfg, stdout)
	if err := postgres.Migrate(ctx, cfg.DatabaseMigrateURL); err != nil {
		logger.ErrorContext(ctx, "migrate database", "error", err)
		return exitError
	}
	logger.InfoContext(ctx, "database migrations applied")
	return exitOK
}

func initialize(ctx context.Context, cfg config.Config, logger *slog.Logger) (string, time.Time, error) {
	db, err := openDatabase(ctx, cfg, logger)
	if err != nil {
		return "", time.Time{}, err
	}
	defer db.Close()
	if _, err := agentca.Bootstrap(ctx, postgres.NewAgentCAStore(db), cfg.MasterKeys, time.Now); err != nil {
		return "", time.Time{}, fmt.Errorf("agent CA: %w", err)
	}
	token, expires, err := newSetupService(db, nil).IssueToken(ctx)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("issue setup token: %w", err)
	}
	return token, expires, nil
}

func newSetupService(db *postgres.DB, hasher setup.Hasher) *setup.Service {
	return setup.NewService(postgres.NewSetupStore(db), hasher, time.Now, access.NewID)
}

// announceSetup logs a fresh setup token while the first installation is open. This is the
// documented way to hand the one-time bootstrap credential to the operator (container logs,
// KONZEPT §16); it is valid for 30 minutes and replaced on every start.
func announceSetup(ctx context.Context, svc *setup.Service, logger *slog.Logger) error {
	required, err := svc.Required(ctx)
	if err != nil {
		return fmt.Errorf("setup state: %w", err)
	}
	if !required {
		return nil
	}
	token, expires, err := svc.IssueToken(ctx)
	if errors.Is(err, setup.ErrAlreadyInitialized) {
		return nil // completed by another instance in the meantime
	}
	if err != nil {
		return fmt.Errorf("issue setup token: %w", err)
	}
	logger.WarnContext(ctx, "setup required: open the web UI and enter the setup token",
		"setup_token", token, "valid_until", expires.Format(time.RFC3339))
	return nil
}

// ensureKeyFile creates a master key file with a new key unless it exists. An existing file is
// never replaced (losing the key loses all encrypted data); it must be a valid key file.
func ensureKeyFile(path string) (bool, error) {
	content, err := os.ReadFile(path) //nolint:gosec // G304: path is the operator's own CLI argument
	switch {
	case err == nil:
		if _, err := secrets.ParseKeys(string(content)); err != nil {
			return false, fmt.Errorf("existing master key file %s: %w", path, err)
		}
		return false, nil
	case !errors.Is(err, fs.ErrNotExist):
		return false, fmt.Errorf("read master key file: %w", err)
	}
	line, err := secrets.GenerateKeyLine(1)
	if err != nil {
		return false, fmt.Errorf("generate master key: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: see above
	if err != nil {
		return false, fmt.Errorf("create master key file: %w", err)
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		_ = f.Close()
		return false, fmt.Errorf("write master key file: %w", err)
	}
	if err := f.Close(); err != nil {
		return false, fmt.Errorf("write master key file: %w", err)
	}
	return true, nil
}

// withMasterKeysFile makes the configuration read the master keys from path.
func withMasterKeysFile(src config.Source, path string) config.Source {
	lookup := src.LookupEnv
	src.LookupEnv = func(key string) (string, bool) {
		switch key {
		case envMasterKeys + "_FILE":
			return path, true
		case envMasterKeys:
			return "", false
		}
		return lookup(key)
	}
	return src
}
