package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/platform/backupfile"
	"github.com/pmoscode/SerMon/server/internal/secrets"
)

func TestBackupKeygen_WritesAnIdentityFileAndShowsThePublicKey(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"backup-keygen"}, envSource(nil), &stdout, &stderr); code != exitOK {
		t.Fatalf("backup-keygen = %d, %s", code, stderr.String())
	}
	if _, err := backupfile.ParseIdentities(stdout.String()); err != nil {
		t.Errorf("stdout is no identity file: %v", err)
	}
	public := strings.TrimSpace(strings.TrimPrefix(strings.SplitN(stderr.String(), "\n", 2)[0], "public key:"))
	if _, err := backupfile.ParseRecipients(public); err != nil || !strings.Contains(stdout.String(), "# public key: "+public) {
		t.Errorf("public key %q: %v", public, err)
	}
	if strings.Contains(stderr.String(), "AGE-SECRET-KEY") {
		t.Error("the secret key is on stderr")
	}
}

func TestBackupCommands_Usage(t *testing.T) {
	for _, args := range [][]string{{"backup"}, {"backup", "--out"}, {"backup", "--out", "x", "y"}, {"restore"}, {"backup-keygen", "x"}} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), args, envSource(nil), &stdout, &stderr); code != exitUsage {
			t.Errorf("%v = %d, want %d", args, code, exitUsage)
		}
	}
}

func TestBackup_NeedsItsConfiguration(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"backup", "--out", "-"}, envSource(nil), &stdout, &stderr)
	if code != exitError || !strings.Contains(stderr.String(), "SERMON_DATABASE_BACKUP_URL") || !strings.Contains(stderr.String(), "SERMON_BACKUP_RECIPIENTS") {
		t.Errorf("backup = %d, %q", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Error("something went to stdout (where the backup goes)")
	}
}

func TestBackup_NeverReplacesAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sermon.backup")
	if err := os.WriteFile(path, []byte("older backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openOutput(path, nil); err == nil {
		t.Error("openOutput() over an existing file must fail")
	}
	content, _ := os.ReadFile(path)
	if string(content) != "older backup" {
		t.Error("the file was changed")
	}
}

// A file of other master keys where the restore would install them refuses before the database.
func TestRestore_RefusesAnotherMasterKeyFile(t *testing.T) {
	dir := t.TempDir()
	existing, _ := secrets.GenerateKeyLine(1)
	given, _ := secrets.GenerateKeyLine(1)
	path := filepath.Join(dir, "master_keys")
	if err := os.WriteFile(path, []byte(existing+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, _, _ := backupfile.GenerateIdentity(time.Now())
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"restore", "--in", filepath.Join(dir, "missing"), "--install-master-keys", path}, envSource(map[string]string{
		"SERMON_DATABASE_MIGRATE_URL": "postgres://sermon_owner@127.0.0.1:1/sermon",
		"SERMON_MASTER_KEYS":          given,
		"SERMON_BACKUP_IDENTITY":      identity,
	}), &stdout, &stderr)
	if code != exitRefused || !strings.Contains(stderr.String(), "another master key file") {
		t.Errorf("restore = %d, %q", code, stderr.String())
	}
	if strings.Contains(stderr.String(), given) || strings.Contains(stderr.String(), existing) {
		t.Error("a master key is in the log")
	}
	if err := installKeyFile(path, existing); err != nil {
		t.Errorf("the same keys are kept: %v", err)
	}
	fresh := filepath.Join(dir, "new_keys")
	if err := installKeyFile(fresh, given+"\n"); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(fresh)
	if content, _ := os.ReadFile(fresh); string(content) != given+"\n" || info.Mode().Perm() != 0o600 {
		t.Errorf("installed %q with mode %v", content, info.Mode().Perm())
	}
}
