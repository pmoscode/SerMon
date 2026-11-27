package backupfile

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/pmoscode/SerMon/server/internal/secrets"
)

// FuzzReader feeds arbitrary content behind the magic into a correctly encrypted file: the
// reader must not panic, must stay within its limits and must reach the end (io.EOF) only with
// the sealed original.
func FuzzReader(f *testing.F) {
	line, err := secrets.GenerateKeyLine(1)
	if err != nil {
		f.Fatal(err)
	}
	keys, err := secrets.ParseKeys(line)
	if err != nil {
		f.Fatal(err)
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		f.Fatal(err)
	}
	h := Header{ID: "0193f5a0000070008000000000000001", CreatedAt: time.Unix(0, 0).UTC(), Migrations: []int64{1}}
	var file bytes.Buffer
	w, err := NewWriter(&file, []age.Recipient{id.Recipient()}, keys, h)
	if err != nil {
		f.Fatal(err)
	}
	_ = w.BeginTable(Table{Name: "users", Columns: []string{"id", "email"}})
	_, _ = io.WriteString(w, "1\talice@example.org\n2\tbob@example.org\n")
	_ = w.EndTable(2)
	_ = w.Sequences([]Sequence{{Name: "users_id_seq", Value: 2, Called: true}})
	if err := w.Close(); err != nil {
		f.Fatal(err)
	}
	plain, err := age.Decrypt(&file, id)
	if err != nil {
		f.Fatal(err)
	}
	gz, err := gzip.NewReader(plain)
	if err != nil {
		f.Fatal(err)
	}
	original, err := io.ReadAll(gz)
	if err != nil {
		f.Fatal(err)
	}
	body := original[len(magic):]
	f.Add(body)
	f.Add(body[:len(body)/2])
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, body []byte) {
		var file bytes.Buffer
		enc, err := age.Encrypt(&file, id.Recipient())
		if err != nil {
			t.Fatal(err)
		}
		gz := gzip.NewWriter(enc)
		_, _ = gz.Write([]byte(magic))
		_, _ = gz.Write(body)
		_ = gz.Close()
		_ = enc.Close()
		r, err := Open(&file, []age.Identity{id}, keys)
		if err != nil {
			return
		}
		for range 10000 {
			item, err := r.Next()
			if errors.Is(err, io.EOF) {
				if !bytes.Equal(append([]byte(magic), body...), original) {
					t.Fatal("reached the end with changed content")
				}
				return
			}
			if err != nil {
				return
			}
			if item.Data != nil {
				if _, err := item.Data.Rows(); err != nil {
					return
				}
			}
		}
		t.Fatal("no end after 10000 items")
	})
}
