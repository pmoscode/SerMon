package secevents

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/secevent"
)

// CSVHeader are the columns of the export.
var CSVHeader = []string{
	"occurred_at", "received_at", "node_id", "hostname", "kind", "user", "remote_ip", "remote_host", "remote_port", "method",
	"invalid_user", "key_type", "key_fingerprint", "tty", "target", "command", "uid", "pid", "process", "unverified", "new_source",
	"country",
}

// CSVCell defuses a text for spreadsheets (CSV injection): a cell starting with =, +, -, @, a
// tab or a carriage return would be read as a formula, so it gets a leading apostrophe. The
// values come from the hosts' logs and may be chosen by an attacker.
func CSVCell(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

func optional(n uint64, set bool) string {
	if !set {
		return ""
	}
	return strconv.FormatUint(n, 10)
}

func csvRow(e secevent.Event, hostname string, country func(netip.Addr) string) []string {
	d := e.Details
	var ip, host, port string
	if r := e.Remote; r != nil {
		if r.IP.IsValid() {
			ip = r.IP.String()
		}
		host, port = r.Host, optional(uint64(r.Port), r.Port != 0)
	}
	var code string
	if r := e.Remote; r != nil && country != nil {
		code = country(r.IP)
	}
	var uid string
	if d.UID != nil {
		uid = strconv.FormatUint(uint64(*d.UID), 10)
	}
	return []string{
		e.OccurredAt.UTC().Format(time.RFC3339Nano), e.ReceivedAt.UTC().Format(time.RFC3339Nano), e.Node.String(),
		CSVCell(hostname), string(e.Kind), CSVCell(e.User), ip, CSVCell(host), port, CSVCell(d.Method),
		strconv.FormatBool(d.InvalidUser), CSVCell(d.KeyType), CSVCell(d.KeyFingerprint), CSVCell(d.TTY), CSVCell(d.Target),
		CSVCell(d.Command), uid, optional(uint64(d.PID), d.PID != 0), CSVCell(d.Process), strconv.FormatBool(e.Unverified),
		strconv.FormatBool(e.NewSource), code,
	}
}

// WriteCSV writes the header and a row per event (with the host names of the nodes and the
// countries of the addresses, country may be nil) to w, as long as the file stays within
// maxBytes; it returns the rows written and whether events were left out.
func WriteCSV(w io.Writer, events []secevent.Event, hostnames map[identity.NodeID]string, country func(netip.Addr) string, maxBytes int) (int, bool, error) {
	var buf bytes.Buffer
	out := csv.NewWriter(&buf)
	out.UseCRLF = true
	if err := out.Write(CSVHeader); err != nil {
		return 0, false, fmt.Errorf("csv header: %w", err)
	}
	out.Flush()
	rows, truncated := 0, false
	for _, e := range events {
		before := buf.Len()
		if err := out.Write(csvRow(e, hostnames[e.Node], country)); err != nil {
			return 0, false, fmt.Errorf("csv row: %w", err)
		}
		out.Flush()
		if buf.Len() > maxBytes {
			buf.Truncate(before)
			truncated = true
			break
		}
		rows++
	}
	if err := out.Error(); err != nil {
		return 0, false, fmt.Errorf("csv: %w", err)
	}
	if _, err := w.Write(buf.Bytes()); err != nil {
		return 0, false, fmt.Errorf("write csv: %w", err)
	}
	return rows, truncated, nil
}
