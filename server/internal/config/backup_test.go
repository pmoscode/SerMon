package config_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/config"
	"github.com/pmoscode/SerMon/server/internal/platform/backupfile"
	"github.com/pmoscode/SerMon/server/internal/secrets"
)

func backupKeys(t *testing.T) (identity, recipient, masterKeys string) {
	t.Helper()
	identity, recipient, err := backupfile.GenerateIdentity(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	masterKeys, err = secrets.GenerateKeyLine(1)
	if err != nil {
		t.Fatal(err)
	}
	return identity, recipient, masterKeys
}

// V1-05: the backup reads only what it needs: the URL of sermon_backup, the master keys and the
// public backup keys – nothing of the server's configuration (SMTP, origins, …).
func TestLoadBackup(t *testing.T) {
	_, recipient, keys := backupKeys(t)
	cfg, err := config.LoadBackup(fakeSource(map[string]string{
		"SERMON_DATABASE_BACKUP_URL_FILE": "/run/secrets/database_backup_url",
		"SERMON_MASTER_KEYS_FILE":         "/var/lib/sermon/master_keys",
		"SERMON_BACKUP_RECIPIENTS_FILE":   "/run/secrets/backup_recipients",
		"SERMON_LOG_FORMAT":               "text",
	}, map[string]string{
		"/run/secrets/database_backup_url": dbURL("sermon_backup") + "\n",
		"/var/lib/sermon/master_keys":      keys + "\n",
		"/run/secrets/backup_recipients":   "# admins\n" + recipient + "\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURL != dbURL("sermon_backup") || cfg.MasterKeys == nil || len(cfg.Recipients) != 1 || cfg.LogFormat != config.LogFormatText {
		t.Errorf("LoadBackup() = %+v", cfg)
	}
}

func TestLoadBackup_Errors(t *testing.T) {
	identity, recipient, keys := backupKeys(t)
	full := map[string]string{
		"SERMON_DATABASE_BACKUP_URL": dbURL("sermon_backup"),
		"SERMON_MASTER_KEYS":         keys,
		"SERMON_BACKUP_RECIPIENTS":   recipient,
	}
	tests := []struct {
		name    string
		change  map[string]string
		without string
		want    string
	}{
		{"no database", nil, "SERMON_DATABASE_BACKUP_URL", "SERMON_DATABASE_BACKUP_URL"},
		{"no master keys", nil, "SERMON_MASTER_KEYS", "SERMON_MASTER_KEYS"},
		{"no backup keys", nil, "SERMON_BACKUP_RECIPIENTS", "SERMON_BACKUP_RECIPIENTS"},
		{"a secret key instead of the public one", map[string]string{"SERMON_BACKUP_RECIPIENTS": identity}, "", "SERMON_BACKUP_RECIPIENTS"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := map[string]string{}
			for k, v := range full {
				if k != tt.without {
					env[k] = v
				}
			}
			for k, v := range tt.change {
				env[k] = v
			}
			_, err := config.LoadBackup(fakeSource(env, nil))
			if !errors.Is(err, config.ErrInvalid) || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("LoadBackup() = %v, want an error about %s", err, tt.want)
			}
			if err != nil && (strings.Contains(err.Error(), testDBPassword) || strings.Contains(err.Error(), "AGE-SECRET-KEY")) {
				t.Errorf("the error shows a secret: %v", err)
			}
		})
	}
}

// The restore takes the owner (it migrates and loads every organization), the master keys and
// the secret backup key.
func TestLoadRestore(t *testing.T) {
	identity, recipient, keys := backupKeys(t)
	env := map[string]string{
		"SERMON_DATABASE_MIGRATE_URL": dbURL("sermon_owner"),
		"SERMON_MASTER_KEYS":          keys,
		"SERMON_BACKUP_IDENTITY":      identity,
	}
	cfg, err := config.LoadRestore(fakeSource(env, nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseMigrateURL != dbURL("sermon_owner") || cfg.MasterKeys == nil || len(cfg.Identities) != 1 || cfg.MasterKeysText != keys {
		t.Errorf("LoadRestore() = %+v", cfg)
	}
	for name, change := range map[string]map[string]string{
		"SERMON_DATABASE_MIGRATE_URL": {"SERMON_DATABASE_MIGRATE_URL": ""},
		"SERMON_BACKUP_IDENTITY":      {"SERMON_BACKUP_IDENTITY": recipient},
		"SERMON_MASTER_KEYS":          {"SERMON_MASTER_KEYS": "1:short"},
	} {
		broken := map[string]string{}
		for k, v := range env {
			broken[k] = v
		}
		for k, v := range change {
			broken[k] = v
		}
		if _, err := config.LoadRestore(fakeSource(broken, nil)); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: LoadRestore() = %v", name, err)
		}
	}
}
