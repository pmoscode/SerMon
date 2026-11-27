// Package backupfile is the file format of "sermon-server backup" (V1-05, ADR 0042).
//
// A backup is an age file (age-encryption.org/v1) to the backup keys of the operator; inside is
// a gzip stream of records:
//
//	"SERMON-BACKUP\n" | H header | (T table | D data… | E end)… | S sequences | Z seal
//
// Each record is its kind (1 byte), the length of its payload (4 bytes, big endian) and the
// payload; JSON for H, T, E and S, the table's COPY text for D. The header carries a value
// sealed with the instance's master keys (secrets.Box): without the keys of the instance the
// restore stops before it touches the database. The seal Z is the SHA-256 of everything before
// it, sealed with the master keys too: a file encrypted by someone with the public backup key
// only, or one cut off, is refused at its end (the restore rolls back).
package backupfile

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"regexp"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/pmoscode/SerMon/server/internal/secrets"
)

// Format is the version of the file format.
const Format = 1

const (
	magic = "SERMON-BACKUP\n"
	// chunkSize is the size of the data records the writer emits.
	chunkSize = 256 << 10
	// maxRecord bounds every record the reader accepts (memory, ADR 0042).
	maxRecord = 1 << 20

	kindHeader    = 'H'
	kindTable     = 'T'
	kindData      = 'D'
	kindEnd       = 'E'
	kindSequences = 'S'
	kindSeal      = 'Z'

	keyCheckContext = "sermon-backup/v1/key-check"
	sealContext     = "sermon-backup/v1/seal/"
)

var (
	// ErrIdentity means none of the backup keys (age identities) opens the file.
	ErrIdentity = errors.New("no backup key opens this backup")
	// ErrMasterKeys means the master keys are not those of the backed-up instance.
	ErrMasterKeys = errors.New("the master keys do not belong to this backup")
	// ErrCorrupt means the file is damaged, cut off, changed or no SerMon backup.
	ErrCorrupt = errors.New("the backup is damaged or was changed")
)

var (
	identifier = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)
	backupID   = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// Header describes a backup.
type Header struct {
	Format        int       `json:"format"`
	ID            string    `json:"id"`
	CreatedAt     time.Time `json:"created_at"`
	ServerVersion string    `json:"server_version"`
	// Migrations are the schema migrations applied in the backed-up database.
	Migrations  []int64 `json:"migrations"`
	Postgres    string  `json:"postgres"`
	TimescaleDB string  `json:"timescaledb"`
	// KeyCheck is the ID sealed with the master keys.
	KeyCheck []byte `json:"key_check"`
}

// Table is a table of the backup and the columns of its data, in their order.
type Table struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
}

// Sequence is the state of a sequence.
type Sequence struct {
	Name   string `json:"name"`
	Value  int64  `json:"value"`
	Called bool   `json:"called"`
}

func (t Table) valid() bool {
	if !identifier.MatchString(t.Name) || len(t.Columns) == 0 {
		return false
	}
	for _, c := range t.Columns {
		if !identifier.MatchString(c) {
			return false
		}
	}
	return true
}

// GenerateIdentity returns a new backup key: the content of its file (an age identity file with
// the public key as a comment) and the public key for SERMON_BACKUP_RECIPIENTS. It is a
// post-quantum hybrid key (ML-KEM-768 + X25519): backups are kept for years.
func GenerateIdentity(now time.Time) (file, recipient string, err error) {
	id, err := age.GenerateHybridIdentity()
	if err != nil {
		return "", "", fmt.Errorf("generate backup key: %w", err)
	}
	recipient = id.Recipient().String()
	file = "# SerMon backup key (age), created " + now.UTC().Format(time.RFC3339) + "\n" +
		"# Keep it away from the server: it opens every backup. The server needs the public key only.\n" +
		"# public key: " + recipient + "\n" + id.String() + "\n"
	return file, recipient, nil
}

// ParseRecipients reads public backup keys (age1… or age1pq1…), one per line; empty lines and
// lines starting with # are ignored.
func ParseRecipients(text string) ([]age.Recipient, error) {
	rs, err := age.ParseRecipients(strings.NewReader(text))
	if err != nil {
		// The error of age may quote the line; it could be a secret key pasted by mistake.
		return nil, errors.New("no valid age public key (age1… or age1pq1…, one per line)")
	}
	for _, r := range rs {
		switch r.(type) {
		case *age.HybridRecipient, *age.X25519Recipient:
		default:
			return nil, errors.New("only native age public keys (age1… or age1pq1…) are allowed")
		}
	}
	return rs, nil
}

// ParseIdentities reads backup keys (AGE-SECRET-KEY-…), one per line; empty lines and lines
// starting with # are ignored. Errors never contain key material.
func ParseIdentities(text string) ([]age.Identity, error) {
	ids, err := age.ParseIdentities(strings.NewReader(text))
	if err != nil {
		return nil, errors.New("no valid age secret key (one per line, as written by backup-keygen or age-keygen)")
	}
	for _, id := range ids {
		switch id.(type) {
		case *age.HybridIdentity, *age.X25519Identity:
		default:
			return nil, errors.New("only native age secret keys are allowed")
		}
	}
	return ids, nil
}

// Writer writes a backup. Call BeginTable, Write and EndTable per table, then Sequences and
// Close; only a closed writer has written a complete backup.
type Writer struct {
	encrypted io.WriteCloser
	gz        *gzip.Writer
	digest    hash.Hash
	out       io.Writer
	keys      *secrets.Box
	id        string
	inTable   bool
	pending   []byte
	err       error
}

// NewWriter starts a backup with header h to dst, encrypted to recipients. ID must be 32
// lowercase hex digits; Format and KeyCheck are set here.
func NewWriter(dst io.Writer, recipients []age.Recipient, keys *secrets.Box, h Header) (*Writer, error) {
	if !backupID.MatchString(h.ID) || len(recipients) == 0 || keys == nil {
		return nil, errors.New("backup: needs an ID, backup keys and the master keys")
	}
	check, err := keys.Seal([]byte(h.ID), []byte(keyCheckContext))
	if err != nil {
		return nil, fmt.Errorf("backup: seal the key check: %w", err)
	}
	h.Format, h.KeyCheck = Format, check
	encrypted, err := age.Encrypt(dst, recipients...)
	if err != nil {
		return nil, fmt.Errorf("backup: encrypt: %w", err)
	}
	gz := gzip.NewWriter(encrypted)
	digest := sha256.New()
	w := &Writer{encrypted: encrypted, gz: gz, digest: digest, out: io.MultiWriter(gz, digest), keys: keys, id: h.ID}
	if _, err := io.WriteString(w.out, magic); err != nil {
		return nil, fmt.Errorf("backup: write: %w", err)
	}
	if err := w.json(kindHeader, h); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *Writer) record(kind byte, payload []byte) error {
	if w.err != nil {
		return w.err
	}
	var head [5]byte
	head[0] = kind
	binary.BigEndian.PutUint32(head[1:], uint32(len(payload))) //nolint:gosec // G115: records are at most maxRecord
	if _, err := w.out.Write(head[:]); err != nil {
		w.err = fmt.Errorf("backup: write: %w", err)
		return w.err
	}
	if _, err := w.out.Write(payload); err != nil {
		w.err = fmt.Errorf("backup: write: %w", err)
	}
	return w.err
}

func (w *Writer) json(kind byte, v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("backup: encode: %w", err)
	}
	return w.record(kind, payload)
}

// BeginTable starts the data of a table.
func (w *Writer) BeginTable(t Table) error {
	if w.inTable || !t.valid() {
		return fmt.Errorf("backup: cannot begin table %q here", t.Name)
	}
	w.inTable = true
	return w.json(kindTable, t)
}

// Write takes the COPY text of the current table.
func (w *Writer) Write(p []byte) (int, error) {
	if !w.inTable {
		return 0, errors.New("backup: data outside a table")
	}
	n := len(p)
	for len(p) > 0 {
		take := min(chunkSize-len(w.pending), len(p))
		w.pending = append(w.pending, p[:take]...)
		p = p[take:]
		if len(w.pending) == chunkSize {
			if err := w.flush(); err != nil {
				return 0, err
			}
		}
	}
	return n, nil
}

func (w *Writer) flush() error {
	if len(w.pending) == 0 {
		return nil
	}
	err := w.record(kindData, w.pending)
	w.pending = w.pending[:0]
	return err
}

// EndTable ends the current table with the number of its rows.
func (w *Writer) EndTable(rows int64) error {
	if !w.inTable {
		return errors.New("backup: no table to end")
	}
	if err := w.flush(); err != nil {
		return err
	}
	w.inTable = false
	return w.json(kindEnd, tableEnd{Rows: rows})
}

// Sequences writes the state of the sequences.
func (w *Writer) Sequences(seqs []Sequence) error {
	if w.inTable {
		return errors.New("backup: sequences inside a table")
	}
	for _, s := range seqs {
		if !identifier.MatchString(s.Name) {
			return fmt.Errorf("backup: invalid sequence name %q", s.Name)
		}
	}
	return w.json(kindSequences, seqs)
}

// Close seals and ends the backup.
func (w *Writer) Close() error {
	if w.inTable {
		return errors.New("backup: a table is still open")
	}
	if w.err != nil {
		return w.err
	}
	sealed, err := w.keys.Seal(w.digest.Sum(nil), []byte(sealContext+w.id))
	if err != nil {
		return fmt.Errorf("backup: seal: %w", err)
	}
	if err := w.record(kindSeal, sealed); err != nil {
		return err
	}
	if err := w.gz.Close(); err != nil {
		return fmt.Errorf("backup: compress: %w", err)
	}
	if err := w.encrypted.Close(); err != nil {
		return fmt.Errorf("backup: encrypt: %w", err)
	}
	return nil
}

type tableEnd struct {
	Rows int64 `json:"rows"`
}

// Reader reads a backup; nothing it returns is trusted before Next reported io.EOF (the seal).
type Reader struct {
	buf *bufio.Reader
	// src reads from buf into the digest: only what was consumed counts.
	src    io.Reader
	digest hash.Hash
	keys   *secrets.Box
	header Header
	table  *TableData
	err    error
}

// Item is the next part of a backup: a table with its data or the sequences.
type Item struct {
	Table     *Table
	Data      *TableData
	Sequences []Sequence
}

// Open decrypts src with the backup keys and checks the master keys against the header.
func Open(src io.Reader, identities []age.Identity, keys *secrets.Box) (*Reader, error) {
	plain, err := age.Decrypt(src, identities...)
	if err != nil {
		var noMatch *age.NoIdentityMatchError
		if errors.As(err, &noMatch) {
			return nil, ErrIdentity
		}
		return nil, fmt.Errorf("%w: %w", ErrCorrupt, err)
	}
	gz, err := gzip.NewReader(plain)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCorrupt, err)
	}
	r := &Reader{digest: sha256.New(), keys: keys}
	r.buf = bufio.NewReader(gz)
	r.src = io.TeeReader(r.buf, r.digest)
	start := make([]byte, len(magic))
	if _, err := io.ReadFull(r.src, start); err != nil || string(start) != magic {
		return nil, fmt.Errorf("%w: no SerMon backup", ErrCorrupt)
	}
	kind, payload, err := r.record()
	if err != nil {
		return nil, err
	}
	if kind != kindHeader || decode(payload, &r.header) != nil || r.header.Format != Format ||
		!backupID.MatchString(r.header.ID) || len(r.header.Migrations) == 0 {
		return nil, fmt.Errorf("%w: unknown header", ErrCorrupt)
	}
	id, err := keys.Open(r.header.KeyCheck, []byte(keyCheckContext))
	if err != nil || string(id) != r.header.ID {
		return nil, ErrMasterKeys
	}
	return r, nil
}

// Header returns the header of the backup.
func (r *Reader) Header() Header { return r.header }

// recordHead reads the kind and length of the next record.
func (r *Reader) recordHead() (byte, int, error) {
	var head [5]byte
	if _, err := io.ReadFull(r.src, head[:]); err != nil {
		return 0, 0, fmt.Errorf("%w: it ends without its seal (%w)", ErrCorrupt, err)
	}
	n := binary.BigEndian.Uint32(head[1:])
	if n > maxRecord {
		return 0, 0, fmt.Errorf("%w: record of %d bytes", ErrCorrupt, n)
	}
	return head[0], int(n), nil
}

func (r *Reader) payload(n int) ([]byte, error) {
	payload := make([]byte, n)
	if _, err := io.ReadFull(r.src, payload); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCorrupt, err)
	}
	return payload, nil
}

func (r *Reader) record() (byte, []byte, error) {
	kind, n, err := r.recordHead()
	if err != nil {
		return 0, nil, err
	}
	payload, err := r.payload(n)
	return kind, payload, err
}

func decode(payload []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(payload))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}

// Next returns the next table or the sequences; io.EOF only at the end with a valid seal. The
// rest of an unread table is skipped.
func (r *Reader) Next() (Item, error) {
	if r.err != nil {
		return Item{}, r.err
	}
	if r.table != nil {
		if _, err := r.table.Rows(); err != nil {
			return Item{}, err
		}
		r.table = nil
	}
	before := r.digest.Sum(nil)
	kind, payload, err := r.record()
	if err != nil {
		return Item{}, r.fail(err)
	}
	switch kind {
	case kindTable:
		var t Table
		if decode(payload, &t) != nil || !t.valid() {
			return Item{}, r.fail(fmt.Errorf("%w: invalid table", ErrCorrupt))
		}
		r.table = &TableData{r: r}
		return Item{Table: &t, Data: r.table}, nil
	case kindSequences:
		var seqs []Sequence
		if decode(payload, &seqs) != nil {
			return Item{}, r.fail(fmt.Errorf("%w: invalid sequences", ErrCorrupt))
		}
		for _, s := range seqs {
			if !identifier.MatchString(s.Name) {
				return Item{}, r.fail(fmt.Errorf("%w: invalid sequence", ErrCorrupt))
			}
		}
		return Item{Sequences: seqs}, nil
	case kindSeal:
		return Item{}, r.fail(r.verify(before, payload))
	}
	return Item{}, r.fail(fmt.Errorf("%w: unexpected record %q", ErrCorrupt, kind))
}

func (r *Reader) verify(digest, sealed []byte) error {
	want, err := r.keys.Open(sealed, []byte(sealContext+r.header.ID))
	if err != nil || !bytes.Equal(want, digest) {
		return fmt.Errorf("%w: the seal does not match", ErrCorrupt)
	}
	if _, err := r.buf.ReadByte(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: data after the seal", ErrCorrupt)
	}
	return io.EOF
}

func (r *Reader) fail(err error) error {
	r.err = err
	return err
}

// TableData is the COPY text of a table.
type TableData struct {
	r    *Reader
	left int // bytes left in the current data record
	rows int64
	done bool
}

// Read reads the table's data; io.EOF at its end.
func (d *TableData) Read(p []byte) (int, error) {
	if d.done {
		return 0, io.EOF
	}
	if d.r.err != nil {
		return 0, d.r.err
	}
	for d.left == 0 {
		kind, n, err := d.r.recordHead()
		if err != nil {
			return 0, d.r.fail(err)
		}
		switch kind {
		case kindData:
			d.left = n
		case kindEnd:
			payload, err := d.r.payload(n)
			var end tableEnd
			if err != nil || decode(payload, &end) != nil || end.Rows < 0 {
				return 0, d.r.fail(fmt.Errorf("%w: invalid end of table", ErrCorrupt))
			}
			d.rows, d.done = end.Rows, true
			return 0, io.EOF
		default:
			return 0, d.r.fail(fmt.Errorf("%w: unexpected record %q in a table", ErrCorrupt, kind))
		}
	}
	n, err := d.r.src.Read(p[:min(len(p), d.left)])
	d.left -= n
	if err != nil && (!errors.Is(err, io.EOF) || n == 0) {
		return n, d.r.fail(fmt.Errorf("%w: %w", ErrCorrupt, err))
	}
	return n, nil
}

// Rows reads the rest of the table and returns the number of rows the backup recorded for it.
func (d *TableData) Rows() (int64, error) {
	if _, err := io.Copy(io.Discard, d); err != nil {
		return 0, fmt.Errorf("read the table: %w", err)
	}
	return d.rows, nil
}
