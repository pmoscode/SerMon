package backupfile_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/pmoscode/SerMon/server/internal/platform/backupfile"
	"github.com/pmoscode/SerMon/server/internal/secrets"
)

func newKeys(t *testing.T) *secrets.Box {
	t.Helper()
	line, err := secrets.GenerateKeyLine(1)
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.ParseKeys(line)
	if err != nil {
		t.Fatal(err)
	}
	return box
}

type backupKey struct {
	identities []age.Identity
	recipients []age.Recipient
}

func newBackupKey(t *testing.T) backupKey {
	t.Helper()
	file, recipient, err := backupfile.GenerateIdentity(time.Date(2026, 11, 27, 3, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	ids, err := backupfile.ParseIdentities(file)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := backupfile.ParseRecipients(recipient + "\n")
	if err != nil {
		t.Fatal(err)
	}
	return backupKey{identities: ids, recipients: rs}
}

var header = backupfile.Header{
	ID: "0193f5a0000070008000000000000001", CreatedAt: time.Date(2026, 11, 27, 3, 0, 0, 0, time.UTC),
	ServerVersion: "1.0.0", Migrations: []int64{1, 2, 71}, Postgres: "17.6", TimescaleDB: "2.32.0",
}

const (
	usersData = "0193f5a0-0000-7000-8000-000000000001\talice@example.org\tAlice\n"
	orgsData  = "0193f5a0-0000-7000-8000-0000000000f0\tExample\n"
)

// write creates a backup of two tables and a sequence.
func write(t *testing.T, key backupKey, keys *secrets.Box) []byte {
	t.Helper()
	var out bytes.Buffer
	w, err := backupfile.NewWriter(&out, key.recipients, keys, header)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.BeginTable(backupfile.Table{Name: "organizations", Columns: []string{"id", "name"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, orgsData); err != nil {
		t.Fatal(err)
	}
	if err := w.EndTable(1); err != nil {
		t.Fatal(err)
	}
	if err := w.BeginTable(backupfile.Table{Name: "users", Columns: []string{"id", "email", "display_name"}}); err != nil {
		t.Fatal(err)
	}
	// Written in pieces, larger than one record: the reader joins them.
	big := strings.Repeat(usersData, 20000)
	if _, err := io.WriteString(w, big); err != nil {
		t.Fatal(err)
	}
	if err := w.EndTable(20000); err != nil {
		t.Fatal(err)
	}
	if err := w.Sequences([]backupfile.Sequence{{Name: "audit_log_id_seq", Value: 42, Called: true}}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

type item struct {
	table backupfile.Table
	data  string
	rows  int64
	seqs  []backupfile.Sequence
}

// readAll reads every item up to the verified end; it returns the items read before an error.
func readAll(r *backupfile.Reader) ([]item, error) {
	var items []item
	for {
		next, err := r.Next()
		if errors.Is(err, io.EOF) {
			return items, nil
		}
		if err != nil {
			return items, err
		}
		if next.Table == nil {
			items = append(items, item{seqs: next.Sequences})
			continue
		}
		data, err := io.ReadAll(next.Data)
		if err != nil {
			return items, err
		}
		rows, err := next.Data.Rows()
		if err != nil {
			return items, err
		}
		items = append(items, item{table: *next.Table, data: string(data), rows: rows})
	}
}

func TestBackup_RoundTripsTablesAndSequences(t *testing.T) {
	key, keys := newBackupKey(t), newKeys(t)
	file := write(t, key, keys)

	r, err := backupfile.Open(bytes.NewReader(file), key.identities, keys)
	if err != nil {
		t.Fatal(err)
	}
	got := r.Header()
	if got.ID != header.ID || got.Format != backupfile.Format || !got.CreatedAt.Equal(header.CreatedAt) ||
		len(got.Migrations) != 3 || got.Migrations[2] != 71 || got.Postgres != "17.6" || got.ServerVersion != "1.0.0" {
		t.Errorf("header = %+v", got)
	}
	items, err := readAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("%d items, want 3", len(items))
	}
	if items[0].table.Name != "organizations" || items[0].data != orgsData || items[0].rows != 1 {
		t.Errorf("organizations = %+v", items[0])
	}
	if items[1].table.Name != "users" || len(items[1].table.Columns) != 3 || items[1].data != strings.Repeat(usersData, 20000) || items[1].rows != 20000 {
		t.Errorf("users: %d bytes, %d rows, columns %v", len(items[1].data), items[1].rows, items[1].table.Columns)
	}
	if s := items[2].seqs; len(s) != 1 || s[0] != (backupfile.Sequence{Name: "audit_log_id_seq", Value: 42, Called: true}) {
		t.Errorf("sequences = %v", s)
	}
}

// The file is encrypted and compressed: nothing of the content is readable.
func TestBackup_HasNoPlaintext(t *testing.T) {
	key, keys := newBackupKey(t), newKeys(t)
	file := write(t, key, keys)
	for _, plain := range []string{"alice@example.org", "Example", "organizations", "audit_log_id_seq", header.ID, "SERMON"} {
		if bytes.Contains(file, []byte(plain)) {
			t.Errorf("%q is readable in the backup", plain)
		}
	}
	if !bytes.HasPrefix(file, []byte("age-encryption.org/v1\n")) {
		t.Errorf("not an age file: %q", file[:min(len(file), 30)])
	}
}

// Another backup key opens nothing.
func TestBackup_RefusesTheWrongBackupKey(t *testing.T) {
	keys := newKeys(t)
	file := write(t, newBackupKey(t), keys)
	if _, err := backupfile.Open(bytes.NewReader(file), newBackupKey(t).identities, keys); !errors.Is(err, backupfile.ErrIdentity) {
		t.Errorf("Open() with another backup key = %v, want ErrIdentity", err)
	}
}

// The backup is useless without the master keys of its instance: refused before any table.
func TestBackup_RefusesOtherMasterKeys(t *testing.T) {
	key := newBackupKey(t)
	file := write(t, key, newKeys(t))
	if _, err := backupfile.Open(bytes.NewReader(file), key.identities, newKeys(t)); !errors.Is(err, backupfile.ErrMasterKeys) {
		t.Errorf("Open() with other master keys = %v, want ErrMasterKeys", err)
	}
}

// A rotated key file (new key first, the old one behind it) still opens older backups.
func TestBackup_OpensWithRotatedMasterKeys(t *testing.T) {
	key := newBackupKey(t)
	oldLine, _ := secrets.GenerateKeyLine(1)
	newLine, _ := secrets.GenerateKeyLine(2)
	old, _ := secrets.ParseKeys(oldLine)
	rotated, _ := secrets.ParseKeys(newLine + "\n" + oldLine)
	file := write(t, key, old)
	r, err := backupfile.Open(bytes.NewReader(file), key.identities, rotated)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readAll(r); err != nil {
		t.Errorf("read with rotated keys: %v", err)
	}
}

// Every changed byte is noticed: age authenticates each chunk.
func TestBackup_RefusesAChangedFile(t *testing.T) {
	key, keys := newBackupKey(t), newKeys(t)
	file := write(t, key, keys)
	for _, at := range []int{len(file) / 2, len(file) - 1} {
		changed := bytes.Clone(file)
		changed[at] ^= 0x01
		r, err := backupfile.Open(bytes.NewReader(changed), key.identities, keys)
		if err == nil {
			_, err = readAll(r)
		}
		if !errors.Is(err, backupfile.ErrCorrupt) {
			t.Errorf("byte %d of %d changed: %v, want ErrCorrupt", at, len(file), err)
		}
	}
}

// A cut-off file ends without its seal: an error, never a silent end.
func TestBackup_RefusesATruncatedFile(t *testing.T) {
	key, keys := newBackupKey(t), newKeys(t)
	file := write(t, key, keys)
	for _, n := range []int{len(file) - 1, len(file) - 100, len(file) / 2} {
		r, err := backupfile.Open(bytes.NewReader(file[:n]), key.identities, keys)
		if err == nil {
			_, err = readAll(r)
		}
		if !errors.Is(err, backupfile.ErrCorrupt) {
			t.Errorf("cut at %d of %d: %v, want ErrCorrupt", n, len(file), err)
		}
	}
}

// Someone with the public backup key but without the master keys can encrypt a file of their own;
// its seal doesn't fit: refused at the end (the restore rolls back).
func TestBackup_RefusesAFileSealedWithOtherMasterKeysBehindAValidHeader(t *testing.T) {
	key, keys := newBackupKey(t), newKeys(t)
	forged := backupfile.ForgeWithForeignSeal(t, key.recipients, keys, newKeys(t), header)
	r, err := backupfile.Open(bytes.NewReader(forged), key.identities, keys)
	if err != nil {
		t.Fatalf("Open() = %v (the header is valid)", err)
	}
	if _, err := readAll(r); !errors.Is(err, backupfile.ErrCorrupt) {
		t.Errorf("forged seal: %v, want ErrCorrupt", err)
	}
}

// Records beyond the limit are refused before they are read into memory.
func TestBackup_RefusesOversizedRecords(t *testing.T) {
	key, keys := newBackupKey(t), newKeys(t)
	file := backupfile.ForgeOversizedRecord(t, key.recipients, keys, header)
	r, err := backupfile.Open(bytes.NewReader(file), key.identities, keys)
	if err == nil {
		_, err = readAll(r)
	}
	if !errors.Is(err, backupfile.ErrCorrupt) {
		t.Errorf("oversized record: %v, want ErrCorrupt", err)
	}
}

// Nothing may follow the seal.
func TestBackup_RefusesDataAfterTheSeal(t *testing.T) {
	key, keys := newBackupKey(t), newKeys(t)
	file := backupfile.ForgeTrailingData(t, key.recipients, keys, header)
	r, err := backupfile.Open(bytes.NewReader(file), key.identities, keys)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readAll(r); !errors.Is(err, backupfile.ErrCorrupt) {
		t.Errorf("data after the seal: %v, want ErrCorrupt", err)
	}
}

// The row count of a table can only be asked after its data.
func TestBackup_RowsNeedTheWholeTable(t *testing.T) {
	key, keys := newBackupKey(t), newKeys(t)
	r, err := backupfile.Open(bytes.NewReader(write(t, key, keys)), key.identities, keys)
	if err != nil {
		t.Fatal(err)
	}
	next, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := next.Data.Rows(); err != nil || rows != 1 {
		t.Errorf("Rows() before reading = %d, %v; want the table drained and 1", rows, err)
	}
	// Next skips the rest of an unread table.
	next, err = r.Next()
	if err != nil || next.Table == nil || next.Table.Name != "users" {
		t.Fatalf("Next() = %+v, %v", next, err)
	}
	next, err = r.Next()
	if err != nil || len(next.Sequences) != 1 {
		t.Fatalf("Next() after skipping = %+v, %v", next, err)
	}
	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("end = %v, want io.EOF", err)
	}
}

func TestWriter_RefusesDataOutsideATable(t *testing.T) {
	key, keys := newBackupKey(t), newKeys(t)
	w, err := backupfile.NewWriter(io.Discard, key.recipients, keys, header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("x")); err == nil {
		t.Error("Write() outside a table must fail")
	}
	if err := w.EndTable(0); err == nil {
		t.Error("EndTable() outside a table must fail")
	}
	_ = w.BeginTable(backupfile.Table{Name: "users", Columns: []string{"id"}})
	if err := w.BeginTable(backupfile.Table{Name: "nodes", Columns: []string{"id"}}); err == nil {
		t.Error("BeginTable() inside a table must fail")
	}
	if err := w.Close(); err == nil {
		t.Error("Close() inside a table must fail")
	}
}

func TestParseRecipients(t *testing.T) {
	_, recipient, err := backupfile.GenerateIdentity(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(recipient, "age1pq1") {
		t.Errorf("recipient %q: want a post-quantum hybrid key (age1pq1…)", recipient[:12])
	}
	x, _ := age.GenerateX25519Identity()
	tests := []struct {
		name string
		text string
		n    int
	}{
		{"hybrid with comments", "# backup key of the admins\n\n" + recipient + "\n", 1},
		{"x25519 (age-keygen)", x.Recipient().String(), 1},
		{"empty", "# nothing\n", 0},
		{"garbage", "not-a-key", 0},
		{"a secret key by mistake", x.String(), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rs, err := backupfile.ParseRecipients(tt.text)
			if tt.n == 0 {
				if err == nil {
					t.Errorf("ParseRecipients() = %d recipients, want an error", len(rs))
				}
				if err != nil && strings.Contains(err.Error(), "AGE-SECRET-KEY") {
					t.Errorf("the error shows the key: %v", err)
				}
				return
			}
			if err != nil || len(rs) != tt.n {
				t.Errorf("ParseRecipients() = %d, %v", len(rs), err)
			}
		})
	}
}

func TestParseIdentities_RefusesPublicKeysAndShowsNoSecret(t *testing.T) {
	file, recipient, _ := backupfile.GenerateIdentity(time.Now())
	if !strings.Contains(file, "# public key: "+recipient) || !strings.Contains(file, "AGE-SECRET-KEY-PQ-1") {
		t.Errorf("identity file:\n%s", file)
	}
	if _, err := backupfile.ParseIdentities(recipient); err == nil {
		t.Error("a public key is no identity")
	}
	broken := strings.Replace(file, "AGE-SECRET-KEY-PQ-1", "AGE-SECRET-KEY-PQ-X", 1)
	if _, err := backupfile.ParseIdentities(broken); err == nil || strings.Contains(err.Error(), "AGE-SECRET") {
		t.Errorf("ParseIdentities(broken) = %v", err)
	}
}
