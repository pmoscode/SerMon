// Package geoip looks up the country of an address in a local MaxMind DB file the operator
// provides, e.g. DB-IP Lite (CC BY 4.0) or GeoLite2 (M3-18, KONZEPT §11). The server downloads
// nothing (D27); without a file the lookup is off.
package geoip

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/oschwald/maxminddb-golang/v2"

	"github.com/pmoscode/SerMon/server/internal/domain/textsafe"
)

// MaxNotice bounds the attribution shown in the UI.
const MaxNotice = 300

// Attributions the licences of the common free databases ask for.
const (
	dbipNotice    = "IP geolocation by DB-IP (https://db-ip.com), licensed under CC BY 4.0."
	geoliteNotice = "This product includes GeoLite2 data created by MaxMind, available from https://www.maxmind.com."
)

// DB is an open country database.
type DB struct {
	reader *maxminddb.Reader
	notice string
}

// ErrNoCountries means the file holds no country data (e.g. an ASN database).
var ErrNoCountries = errors.New("geoip: the database has no countries")

// Open opens the file at path; attribution overrides the notice derived from the database.
func Open(path, attribution string) (*DB, error) {
	r, err := maxminddb.Open(path)
	if err != nil {
		return nil, fmt.Errorf("geoip: open %s: %w", path, err)
	}
	return newDB(r, attribution)
}

// OpenBytes opens a database held in memory (tests).
func OpenBytes(data []byte, attribution string) (*DB, error) {
	r, err := maxminddb.OpenBytes(data)
	if err != nil {
		return nil, fmt.Errorf("geoip: open: %w", err)
	}
	return newDB(r, attribution)
}

func newDB(r *maxminddb.Reader, attribution string) (*DB, error) {
	t := strings.ToLower(r.Metadata.DatabaseType)
	if !strings.Contains(t, "country") && !strings.Contains(t, "city") {
		_ = r.Close()
		return nil, fmt.Errorf("%w (type %q)", ErrNoCountries, r.Metadata.DatabaseType)
	}
	return &DB{reader: r, notice: notice(r.Metadata, attribution)}, nil
}

func notice(m maxminddb.Metadata, attribution string) string {
	text := strings.TrimSpace(attribution)
	t := strings.ToLower(m.DatabaseType)
	switch {
	case text != "":
	case strings.HasPrefix(t, "dbip"):
		text = dbipNotice
	case strings.HasPrefix(t, "geolite2"):
		text = geoliteNotice
	case m.Description["en"] != "":
		text = m.Description["en"]
	default:
		text = m.DatabaseType
	}
	text = textsafe.Visible(text)
	for len(text) > MaxNotice {
		_, size := utf8.DecodeLastRuneInString(text)
		text = text[:len(text)-size]
	}
	return text
}

// Close releases the file.
func (d *DB) Close() error {
	if err := d.reader.Close(); err != nil {
		return fmt.Errorf("geoip: close: %w", err)
	}
	return nil
}

// Notice is the attribution the licence asks for, shown next to the countries.
func (d *DB) Notice() string { return d.notice }

// Type and BuildTime describe the file (logged at the start).
func (d *DB) Type() string { return d.reader.Metadata.DatabaseType }

// BuildTime is when the file was built.
func (d *DB) BuildTime() time.Time { return d.reader.Metadata.BuildTime() }

var shared = netip.MustParsePrefix("100.64.0.0/10")

// public reports whether an address belongs to the internet: private, loopback, link-local,
// shared (CGNAT) and other special addresses have no country.
func public(ip netip.Addr) bool {
	return ip.IsValid() && ip.IsGlobalUnicast() && !ip.IsPrivate() && !shared.Contains(ip)
}

type record struct {
	Country struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"country"`
	Registered struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"registered_country"`
}

func isoCode(s string) bool {
	return len(s) == 2 && s[0] >= 'A' && s[0] <= 'Z' && s[1] >= 'A' && s[1] <= 'Z'
}

// Country returns the ISO 3166-1 alpha-2 code of the address, empty if unknown or not public.
func (d *DB) Country(ip netip.Addr) string {
	ip = ip.Unmap().WithZone("")
	if d == nil || !public(ip) {
		return ""
	}
	var r record
	if err := d.reader.Lookup(ip).Decode(&r); err != nil {
		return ""
	}
	for _, code := range []string{r.Country.ISOCode, r.Registered.ISOCode} {
		if isoCode(code) {
			return code
		}
	}
	return ""
}
