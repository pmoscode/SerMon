package backupfile

import (
	"bytes"
	"testing"

	"filippo.io/age"

	"github.com/pmoscode/SerMon/server/internal/secrets"
)

// ForgeWithForeignSeal writes a backup with a valid header whose seal comes from other keys:
// what someone could build from a real header and the public backup key.
func ForgeWithForeignSeal(t *testing.T, recipients []age.Recipient, keys, other *secrets.Box, h Header) []byte {
	t.Helper()
	var out bytes.Buffer
	w, err := NewWriter(&out, recipients, keys, h)
	if err != nil {
		t.Fatal(err)
	}
	w.keys = other
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// ForgeTrailingData writes a complete backup with data after its seal.
func ForgeTrailingData(t *testing.T, recipients []age.Recipient, keys *secrets.Box, h Header) []byte {
	t.Helper()
	var out bytes.Buffer
	w, err := NewWriter(&out, recipients, keys, h)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := keys.Seal(w.digest.Sum(nil), []byte(sealContext+h.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err := w.record(kindSeal, sealed); err != nil {
		t.Fatal(err)
	}
	if _, err := w.out.Write([]byte("more")); err != nil {
		t.Fatal(err)
	}
	if err := w.gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.encrypted.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// ForgeOversizedRecord writes a table whose data record exceeds the limit of the reader; the
// file is sealed correctly, only the limit refuses it.
func ForgeOversizedRecord(t *testing.T, recipients []age.Recipient, keys *secrets.Box, h Header) []byte {
	t.Helper()
	var out bytes.Buffer
	w, err := NewWriter(&out, recipients, keys, h)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.BeginTable(Table{Name: "users", Columns: []string{"id"}}); err != nil {
		t.Fatal(err)
	}
	if err := w.record(kindData, make([]byte, maxRecord+1)); err != nil {
		t.Fatal(err)
	}
	if err := w.EndTable(1); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
