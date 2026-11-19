// Package pgtest starts a throwaway TimescaleDB container for integration tests
// (build tag "integration", Testcontainers). The image is read from deploy/dev/compose.yaml,
// so tests and the development environment always use the same pinned image.
package pgtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// Database holds the connection strings of the test database.
type Database struct {
	// OwnerDSN connects as the superuser that owns the schema – for migrations and fixtures only.
	OwnerDSN string
	// AppDSN connects as sermon_app, the unprivileged role sermon-server uses (RLS applies).
	AppDSN string
}

var (
	once     sync.Once
	shared   Database
	startErr error
)

// Shared starts one container per test binary (on first use) and returns its connection strings.
// The schema is NOT migrated; call postgres.Migrate with OwnerDSN and then EnableAppRole.
func Shared(t testing.TB) Database {
	t.Helper()
	once.Do(func() { shared, startErr = start(context.Background()) })
	if startErr != nil {
		t.Fatalf("start test database: %v", startErr)
	}
	return shared
}

// EnableAppRole grants LOGIN to sermon_app (created by the migrations) with a random password
// and returns the matching connection string.
func EnableAppRole(t testing.TB, db Database) string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, db.OwnerDSN)
	if err != nil {
		t.Fatalf("connect as owner: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	password := randomHex(t)
	// DDL cannot take parameters; the password is hex only and therefore safe to embed.
	if _, err := conn.Exec(ctx, "ALTER ROLE sermon_app LOGIN PASSWORD '"+password+"'"); err != nil {
		t.Fatalf("enable sermon_app: %v", err)
	}
	u, err := url.Parse(db.OwnerDSN)
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	u.User = url.UserPassword("sermon_app", password)
	return u.String()
}

func start(ctx context.Context) (Database, error) {
	image, err := imageFromCompose()
	if err != nil {
		return Database{}, err
	}
	container, err := tcpostgres.Run(ctx, image,
		tcpostgres.WithDatabase("sermon"),
		tcpostgres.WithUsername("sermon"),
		tcpostgres.WithPassword("sermon-test"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		return Database{}, fmt.Errorf("run container: %w", err)
	}
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return Database{}, fmt.Errorf("connection string: %w", err)
	}
	return Database{OwnerDSN: dsn}, nil
}

var composeImage = regexp.MustCompile(`image:\s*(timescale/timescaledb:\S+)`)

func imageFromCompose() (string, error) {
	_, file, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "..", "deploy", "dev", "compose.yaml")
	content, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	match := composeImage.FindSubmatch(content)
	if match == nil {
		return "", fmt.Errorf("no timescale/timescaledb image in %s", path)
	}
	return string(match[1]), nil
}

func randomHex(t testing.TB) string {
	t.Helper()
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("random password: %v", err)
	}
	return hex.EncodeToString(b)
}
