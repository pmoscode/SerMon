package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/setup"
	"github.com/pmoscode/SerMon/server/internal/secrets"
)

func TestEnsureKeyFile_CreatesAPrivateKeyFileOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master_keys")

	created, err := ensureKeyFile(path)
	if err != nil || !created {
		t.Fatalf("ensureKeyFile() = %v, %v; want created", created, err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	first, _ := os.ReadFile(path)
	if _, err := secrets.ParseKeys(string(first)); err != nil {
		t.Errorf("created file is not a valid key file: %v", err)
	}

	created, err = ensureKeyFile(path)
	second, _ := os.ReadFile(path)
	if err != nil || created || !bytes.Equal(first, second) {
		t.Errorf("second run: created=%v err=%v, changed=%v; an existing key must never be replaced", created, err, !bytes.Equal(first, second))
	}
}

func TestEnsureKeyFile_RejectsAnInvalidExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master_keys")
	if err := os.WriteFile(path, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureKeyFile(path); err == nil {
		t.Error("an invalid existing key file must be reported")
	}
}

func TestRunInit_RequiresADatabase(t *testing.T) {
	var stdout, stderr bytes.Buffer
	path := filepath.Join(t.TempDir(), "master_keys")

	src := envSource(nil)
	src.ReadFile = os.ReadFile
	code := run(context.Background(), []string{"init", "--master-keys-file", path}, src, &stdout, &stderr)
	if code != exitError || !strings.Contains(stderr.String(), "SERMON_DATABASE_URL") {
		t.Errorf("run(init) = %d, stderr %q; want an error naming SERMON_DATABASE_URL", code, stderr.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the key file should be created before the database is needed: %v", err)
	}
}

func TestRunInit_RejectsUnknownFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"init", "--bogus"}, envSource(nil), &stdout, &stderr); code != exitUsage {
		t.Errorf("run(init --bogus) = %d, want %d", code, exitUsage)
	}
}

// migrate (M2-36) runs only the migrations, as the one-shot service of the production compose:
// the long-running server no longer holds the owner's credentials.
func TestRunMigrate_RequiresTheMigrateURL(t *testing.T) {
	var stdout, stderr bytes.Buffer
	src := envSource(map[string]string{"SERMON_MODE": "development"})
	if code := run(context.Background(), []string{"migrate"}, src, &stdout, &stderr); code != exitError || !strings.Contains(stderr.String(), "SERMON_DATABASE_MIGRATE_URL") {
		t.Errorf("run(migrate) = %d, stderr %q; want an error naming SERMON_DATABASE_MIGRATE_URL", code, stderr.String())
	}
	if code := run(context.Background(), []string{"migrate", "now"}, src, &stdout, &stderr); code != exitUsage {
		t.Errorf("run(migrate now) = %d, want %d", code, exitUsage)
	}
}

type memSetupStore struct{ completed bool }

func (m *memSetupStore) Completed(context.Context) (bool, error) { return m.completed, nil }
func (m *memSetupStore) IssueToken(context.Context, [32]byte, time.Time) error {
	if m.completed {
		return setup.ErrAlreadyInitialized
	}
	return nil
}

func (m *memSetupStore) Complete(context.Context, [32]byte, time.Time, setup.FirstAdmin) error {
	return nil
}

func TestAnnounceSetup_LogsATokenOnlyWhileSetupIsOpen(t *testing.T) {
	for _, completed := range []bool{false, true} {
		var log bytes.Buffer
		svc := setup.NewService(&memSetupStore{completed: completed}, nil, time.Now, uuid.New)
		if err := announceSetup(context.Background(), svc, newTestLogger(&log)); err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(log.String(), "setup_token="); got == completed {
			t.Errorf("completed=%v: token logged = %v\n%s", completed, got, log.String())
		}
	}
}

func newTestLogger(w *bytes.Buffer) *slog.Logger { return slog.New(slog.NewTextHandler(w, nil)) }
