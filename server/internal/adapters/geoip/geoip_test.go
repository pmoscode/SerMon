package geoip_test

import (
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pmoscode/SerMon/server/internal/adapters/geoip"
)

var networks = map[string]string{
	"81.2.69.0/24":     "GB",
	"89.160.20.0/22":   "SE",
	"2a02:2770::/32":   "DE",
	"10.0.0.0/8":       "XX", // a private network in the file: never answered
	"100.64.0.0/10":    "XX", // shared address space (CGNAT) in the file
	"203.0.113.0/24":   "us", // not an ISO code
	"198.51.100.64/26": "JP",
	"185.0.0.0/16":     "registered:FR", // only the country the network is registered in
}

func open(t *testing.T, dbType, attribution string) *geoip.DB {
	t.Helper()
	db, err := geoip.OpenBytes(mmdb(dbType, "Test countries", networks), attribution)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestCountry_LooksUpPublicAddresses(t *testing.T) {
	db := open(t, "DBIP-Country-Lite", "")
	for ip, want := range map[string]string{
		"81.2.69.142":          "GB",
		"89.160.23.1":          "SE",
		"::ffff:81.2.69.1":     "GB", // IPv4-mapped
		"2a02:2770:8::1":       "DE",
		"198.51.100.70":        "JP",
		"198.51.100.1":         "", // outside the /26
		"8.8.8.8":              "", // not in the file
		"10.1.2.3":             "", // private
		"192.168.1.1":          "",
		"100.64.1.1":           "", // shared address space (CGNAT)
		"127.0.0.1":            "",
		"fe80::1":              "",
		"fd00::1":              "",
		"203.0.113.9":          "", // the record is no ISO code
		"2a02:2770::1%eth0":    "DE",
		"::":                   "",
		"255.255.255.255":      "",
		"2001:db8:ffff::ffff":  "",
		"81.2.69.255":          "GB",
		"::ffff:10.0.0.1":      "",
		"2a02:2771::1":         "",
		"89.160.19.255":        "",
		"89.160.24.0":          "",
		"2a02:2770:ffff::ffff": "DE",
		"185.0.7.7":            "FR",
	} {
		if got := db.Country(netip.MustParseAddr(ip)); got != want {
			t.Errorf("Country(%s) = %q, want %q", ip, got, want)
		}
	}
	if got := db.Country(netip.Addr{}); got != "" {
		t.Errorf("Country(invalid) = %q", got)
	}
	var off *geoip.DB
	if got := off.Country(netip.MustParseAddr("81.2.69.1")); got != "" {
		t.Errorf("without a database: %q", got)
	}
}

func TestNotice_NamesTheSourceOfTheData(t *testing.T) {
	for dbType, want := range map[string]string{
		"DBIP-Country-Lite":  "IP geolocation by DB-IP (https://db-ip.com), licensed under CC BY 4.0.",
		"GeoLite2-Country":   "This product includes GeoLite2 data created by MaxMind",
		"Acme-Country":       "Test countries",
		"Example-City-Plain": "Test countries",
	} {
		if got := open(t, dbType, "").Notice(); !strings.HasPrefix(got, want) {
			t.Errorf("%s: Notice() = %q, want %q", dbType, got, want)
		}
	}
	if got := open(t, "DBIP-Country-Lite", "  Länderdaten: DB-IP.com \u202E ").Notice(); !strings.HasPrefix(got, "Länderdaten: DB-IP.com ") ||
		strings.Contains(got, "\u202E") {
		t.Errorf("attribution = %q", got)
	}
	if got := open(t, "Acme-Country", strings.Repeat("ä", 400)).Notice(); len(got) > geoip.MaxNotice {
		t.Errorf("a notice of %d bytes", len(got))
	}
}

func TestOpen_RefusesFilesWithoutCountries(t *testing.T) {
	if _, err := geoip.OpenBytes(mmdb("GeoLite2-ASN", "ASN", networks), ""); !errors.Is(err, geoip.ErrNoCountries) {
		t.Errorf("an ASN database: %v", err)
	}
	if _, err := geoip.OpenBytes([]byte("not a database"), ""); err == nil {
		t.Error("garbage opened")
	}
	path := filepath.Join(t.TempDir(), "countries.mmdb")
	if err := os.WriteFile(path, mmdb("DBIP-Country-Lite", "x", networks), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := geoip.Open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if db.Type() != "DBIP-Country-Lite" || db.BuildTime().Year() != 2025 || db.Country(netip.MustParseAddr("81.2.69.1")) != "GB" {
		t.Errorf("from the file: %s %v", db.Type(), db.BuildTime())
	}
	if _, err := geoip.Open(filepath.Join(t.TempDir(), "missing.mmdb"), ""); err == nil {
		t.Error("a missing file opened")
	}
}
