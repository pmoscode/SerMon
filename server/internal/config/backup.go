package config

import (
	"errors"
	"fmt"
	"log/slog"

	"filippo.io/age"

	"github.com/pmoscode/SerMon/server/internal/platform/backupfile"
	"github.com/pmoscode/SerMon/server/internal/secrets"
)

// Variables of the backup and the restore (V1-05, ADR 0042).
const (
	envBackupURL        = "SERMON_DATABASE_BACKUP_URL"
	envBackupRecipients = "SERMON_BACKUP_RECIPIENTS"
	envBackupIdentity   = "SERMON_BACKUP_IDENTITY"
)

// Backup is the configuration of "sermon-server backup".
type Backup struct {
	LogLevel  slog.Level
	LogFormat LogFormat
	// DatabaseURL connects as sermon_backup (read-only, BYPASSRLS).
	DatabaseURL string
	// MasterKeys bind the backup to the instance.
	MasterKeys *secrets.Box
	// Recipients are the public backup keys the file is encrypted to.
	Recipients []age.Recipient
}

// Restore is the configuration of "sermon-server restore".
type Restore struct {
	LogLevel  slog.Level
	LogFormat LogFormat
	// DatabaseMigrateURL connects as the owner of the schema.
	DatabaseMigrateURL string
	MasterKeys         *secrets.Box
	// MasterKeysText is the key file as read, for "restore --install-master-keys".
	MasterKeysText string
	// Identities are the secret backup keys.
	Identities []age.Identity
}

// LoadBackup reads the configuration of the backup; it ignores the server's settings.
func LoadBackup(src Source) (Backup, error) {
	l := loader{src: src}
	cfg := Backup{
		LogLevel:    parseLevel(&l, l.string(envLogLevel, defaultLogLevel, nil)),
		LogFormat:   parseFormat(&l, l.string(envLogFormat, defaultLogFormat, nil)),
		DatabaseURL: required(&l, envBackupURL, "the role sermon_backup"),
		MasterKeys:  parseMasterKeys(&l),
	}
	if cfg.MasterKeys == nil {
		l.fail(envMasterKeys, "required: the backup is bound to the master keys (set %s_FILE)", envMasterKeys)
	}
	if text := required(&l, envBackupRecipients, "the public backup keys, from sermon-server backup-keygen"); text != "" {
		rs, err := backupfile.ParseRecipients(text)
		if err != nil {
			l.fail(envBackupRecipients, "%v", err)
		}
		cfg.Recipients = rs
	}
	if len(l.errs) > 0 {
		return Backup{}, fmt.Errorf("%w: %w", ErrInvalid, errors.Join(l.errs...))
	}
	return cfg, nil
}

// LoadRestore reads the configuration of the restore; it ignores the server's settings.
func LoadRestore(src Source) (Restore, error) {
	l := loader{src: src}
	cfg := Restore{
		LogLevel:           parseLevel(&l, l.string(envLogLevel, defaultLogLevel, nil)),
		LogFormat:          parseFormat(&l, l.string(envLogFormat, defaultLogFormat, nil)),
		DatabaseMigrateURL: required(&l, envMigrateURL, "the owner of the schema"),
	}
	cfg.MasterKeysText, _ = l.raw(envMasterKeys)
	if cfg.MasterKeysText == "" {
		l.fail(envMasterKeys, "required: the master keys of the backed-up instance (set %s_FILE)", envMasterKeys)
	} else if box, err := secrets.ParseKeys(cfg.MasterKeysText); err != nil {
		l.fail(envMasterKeys, "%v", err)
	} else {
		cfg.MasterKeys = box
	}
	if text := required(&l, envBackupIdentity, "the secret backup key"); text != "" {
		ids, err := backupfile.ParseIdentities(text)
		if err != nil {
			l.fail(envBackupIdentity, "%v", err)
		}
		cfg.Identities = ids
	}
	if len(l.errs) > 0 {
		return Restore{}, fmt.Errorf("%w: %w", ErrInvalid, errors.Join(l.errs...))
	}
	return cfg, nil
}

// required returns the value of key or reports it missing; the message never shows the value.
func required(l *loader, key, what string) string {
	value, _ := l.raw(key)
	if value == "" {
		l.fail(key, "required: %s (or %s%s)", what, key, fileSuffix)
	}
	return value
}
