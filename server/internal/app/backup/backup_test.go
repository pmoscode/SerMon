package backup_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/pmoscode/SerMon/server/internal/app/backup"
	"github.com/pmoscode/SerMon/server/internal/platform/backupfile"
	"github.com/pmoscode/SerMon/server/internal/secrets"
)

// fakeSource is a database of two tables and a sequence.
type fakeSource struct {
	tables map[string]string
	order  []backupfile.Table
	failAt string
}

func newSource() *fakeSource {
	return &fakeSource{
		tables: map[string]string{"organizations": "o1\tExample\n", "users": "u1\talice@example.org\nu2\tbob@example.org\n"},
		order: []backupfile.Table{
			{Name: "organizations", Columns: []string{"id", "name"}},
			{Name: "users", Columns: []string{"id", "email"}},
		},
	}
}

func (s *fakeSource) Info(context.Context) (backup.Info, error) {
	return backup.Info{Migrations: []int64{1, 2, 3}, Postgres: "17.6", TimescaleDB: "2.32.0"}, nil
}

func (s *fakeSource) Tables(context.Context) ([]backupfile.Table, error) { return s.order, nil }

func (s *fakeSource) CopyOut(_ context.Context, t backupfile.Table, w io.Writer) (int64, error) {
	if t.Name == s.failAt {
		return 0, errors.New("connection lost")
	}
	data := s.tables[t.Name]
	_, err := io.WriteString(w, data)
	return int64(strings.Count(data, "\n")), err
}

func (s *fakeSource) Sequences(context.Context) ([]backupfile.Sequence, error) {
	return []backupfile.Sequence{{Name: "audit_log_id_seq", Value: 7, Called: true}}, nil
}

// fakeTarget records what a restore does; a failing step stops it.
type fakeTarget struct {
	steps     []string
	prepared  []int64
	loaded    map[string]string
	sequences []backupfile.Sequence
	committed bool
	restored  string
	refuse    error
	finished  bool
	// countOff makes CopyIn report another number of rows than it got.
	countOff int64
}

func (f *fakeTarget) Prepare(_ context.Context, migrations []int64) error {
	f.steps = append(f.steps, "prepare")
	f.prepared = migrations
	return f.refuse
}

func (f *fakeTarget) Load(_ context.Context, fn func(backup.Loader) error) error {
	f.steps = append(f.steps, "load")
	staged := &fakeLoader{target: f, tables: map[string]string{}}
	if err := fn(staged); err != nil {
		f.steps = append(f.steps, "rollback")
		return err
	}
	f.loaded, f.sequences, f.committed, f.restored = staged.tables, staged.sequences, true, staged.restored
	f.steps = append(f.steps, "commit")
	return nil
}

func (f *fakeTarget) Finish(context.Context) error {
	f.steps = append(f.steps, "finish")
	f.finished = true
	return nil
}

type fakeLoader struct {
	target    *fakeTarget
	tables    map[string]string
	sequences []backupfile.Sequence
	restored  string
}

func (l *fakeLoader) Restored(h backupfile.Header) { l.restored = h.ID }

func (l *fakeLoader) CopyIn(_ context.Context, t backupfile.Table, data io.Reader) (int64, error) {
	b, err := io.ReadAll(data)
	if err != nil {
		return 0, err
	}
	l.tables[t.Name] = string(b)
	return int64(bytes.Count(b, []byte("\n"))) + l.target.countOff, nil
}

func (l *fakeLoader) SetSequences(_ context.Context, seqs []backupfile.Sequence) error {
	l.sequences = seqs
	return nil
}

type env struct {
	keys       *secrets.Box
	identities []age.Identity
	recipients []age.Recipient
}

func newEnv(t *testing.T) env {
	t.Helper()
	line, _ := secrets.GenerateKeyLine(1)
	keys, err := secrets.ParseKeys(line)
	if err != nil {
		t.Fatal(err)
	}
	file, recipient, err := backupfile.GenerateIdentity(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ids, _ := backupfile.ParseIdentities(file)
	rs, _ := backupfile.ParseRecipients(recipient)
	return env{keys: keys, identities: ids, recipients: rs}
}

var now = time.Date(2026, 11, 27, 3, 0, 0, 0, time.UTC)

func create(t *testing.T, e env, src backup.Source) ([]byte, backup.Summary) {
	t.Helper()
	var out bytes.Buffer
	summary, err := backup.Create(context.Background(), src, &out, backup.CreateOptions{
		Recipients: e.recipients, Keys: e.keys, ServerVersion: "1.0.0", Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return out.Bytes(), summary
}

func restore(e env, file []byte, target backup.Target) (backupfile.Header, backup.Summary, error) {
	return backup.Restore(context.Background(), bytes.NewReader(file), target, backup.RestoreOptions{Identities: e.identities, Keys: e.keys})
}

func TestCreateAndRestore_CarryTablesSequencesAndMigrations(t *testing.T) {
	e := newEnv(t)
	var progress []string
	var out bytes.Buffer
	created, err := backup.Create(context.Background(), newSource(), &out, backup.CreateOptions{
		Recipients: e.recipients, Keys: e.keys, ServerVersion: "1.0.0", Now: now,
		Progress: func(table string, _ int64) { progress = append(progress, table) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Tables != 2 || created.Rows != 3 || created.ID == "" || strings.Join(progress, ",") != "organizations,users" {
		t.Errorf("created = %+v, progress %v", created, progress)
	}

	target := &fakeTarget{}
	h, restored, err := restore(e, out.Bytes(), target)
	if err != nil {
		t.Fatal(err)
	}
	if h.ID != created.ID || h.ServerVersion != "1.0.0" || !h.CreatedAt.Equal(now) || h.Postgres != "17.6" {
		t.Errorf("header = %+v", h)
	}
	if restored != (backup.Summary{ID: created.ID, Tables: 2, Rows: 3}) {
		t.Errorf("restored = %+v", restored)
	}
	if strings.Join(target.steps, ",") != "prepare,load,commit,finish" {
		t.Errorf("steps = %v", target.steps)
	}
	if len(target.prepared) != 3 || target.prepared[2] != 3 {
		t.Errorf("prepared for %v, want the migrations of the backup", target.prepared)
	}
	if target.loaded["users"] != newSource().tables["users"] || target.loaded["organizations"] != "o1\tExample\n" {
		t.Errorf("loaded = %v", target.loaded)
	}
	if target.restored != created.ID {
		t.Errorf("audited %q, want the backup %q", target.restored, created.ID)
	}
	if len(target.sequences) != 1 || target.sequences[0].Value != 7 {
		t.Errorf("sequences = %v", target.sequences)
	}
}

// Wrong keys are noticed before the database is touched.
func TestRestore_WrongKeysTouchNothing(t *testing.T) {
	e := newEnv(t)
	file, _ := create(t, e, newSource())
	other := newEnv(t)

	target := &fakeTarget{}
	if _, _, err := restore(env{keys: e.keys, identities: other.identities}, file, target); !errors.Is(err, backupfile.ErrIdentity) {
		t.Errorf("another backup key: %v", err)
	}
	if _, _, err := restore(env{keys: other.keys, identities: e.identities}, file, target); !errors.Is(err, backupfile.ErrMasterKeys) {
		t.Errorf("other master keys: %v", err)
	}
	if len(target.steps) != 0 {
		t.Errorf("the target was touched: %v", target.steps)
	}
}

// A database with data refuses before anything is loaded.
func TestRestore_RefusedByTheTargetLoadsNothing(t *testing.T) {
	e := newEnv(t)
	file, _ := create(t, e, newSource())
	target := &fakeTarget{refuse: backup.ErrNotEmpty}
	if _, _, err := restore(e, file, target); !errors.Is(err, backup.ErrNotEmpty) {
		t.Errorf("Restore() = %v, want ErrNotEmpty", err)
	}
	if strings.Join(target.steps, ",") != "prepare" {
		t.Errorf("steps = %v", target.steps)
	}
}

// A changed or cut-off file rolls the load back and leaves the migrations of later versions out.
// bigSource has a table of incompressible rows over several chunks of age (64 KiB each): damage
// at the end is noticed after the header, while loading.
func bigSource() *fakeSource {
	src := newSource()
	var rows strings.Builder
	for range 4000 {
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		rows.WriteString(hex.EncodeToString(b) + "\n")
	}
	src.tables["events"] = rows.String()
	src.order = append(src.order, backupfile.Table{Name: "events", Columns: []string{"id"}})
	return src
}

func TestRestore_ADamagedFileRollsBack(t *testing.T) {
	e := newEnv(t)
	file, _ := create(t, e, bigSource())
	for name, damaged := range map[string][]byte{
		"cut off":   file[:len(file)-20],
		"bit flips": flip(file, len(file)-40),
	} {
		t.Run(name, func(t *testing.T) {
			target := &fakeTarget{}
			if _, _, err := restore(e, damaged, target); !errors.Is(err, backupfile.ErrCorrupt) {
				t.Errorf("Restore() = %v, want ErrCorrupt", err)
			}
			if target.committed || target.finished || len(target.steps) == 0 || target.steps[len(target.steps)-1] != "rollback" {
				t.Errorf("steps = %v", target.steps)
			}
		})
	}
}

func flip(b []byte, at int) []byte {
	c := bytes.Clone(b)
	c[at] ^= 0x80
	return c
}

// The database must take exactly the rows the backup recorded.
func TestRestore_ADifferentRowCountRollsBack(t *testing.T) {
	e := newEnv(t)
	file, _ := create(t, e, newSource())
	target := &fakeTarget{countOff: 1}
	if _, _, err := restore(e, file, target); !errors.Is(err, backupfile.ErrCorrupt) {
		t.Errorf("Restore() = %v, want ErrCorrupt", err)
	}
	if target.committed {
		t.Error("committed")
	}
}

// A failing table ends the backup with an error; the caller discards the file.
func TestCreate_StopsAtAFailingTable(t *testing.T) {
	e := newEnv(t)
	src := newSource()
	src.failAt = "users"
	_, err := backup.Create(context.Background(), src, io.Discard, backup.CreateOptions{Recipients: e.recipients, Keys: e.keys, Now: now})
	if err == nil || !strings.Contains(err.Error(), "users") {
		t.Errorf("Create() = %v, want the table in the error", err)
	}
}

// Every backup gets a new ID.
func TestCreate_NewIDPerBackup(t *testing.T) {
	e := newEnv(t)
	_, a := create(t, e, newSource())
	_, b := create(t, e, newSource())
	if a.ID == b.ID || len(a.ID) != 32 {
		t.Errorf("IDs %q and %q", a.ID, b.ID)
	}
}
