package secevents_test

import (
	"bytes"
	"encoding/csv"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/app/secevents"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/secevent"
)

func TestCSVCell_DefusesFormulas(t *testing.T) {
	for in, want := range map[string]string{
		"alice":                      "alice",
		"=HYPERLINK(\"http://x\")":   "'=HYPERLINK(\"http://x\")",
		"+1+1":                       "'+1+1",
		"-2+3":                       "'-2+3",
		"@SUM(A1)":                   "'@SUM(A1)",
		"\t=1":                       "'\t=1",
		"\r=1":                       "'\r=1",
		"":                           "",
		"a=b":                        "a=b",
		"SHA256:abc+def/ghi=":        "SHA256:abc+def/ghi=",
		"\uff1d1 (fullwidth equals)": "\uff1d1 (fullwidth equals)",
		// M3-28: German Excel splits at ";" – every part is a cell of its own.
		"x;=1+1;":     "x;'=1+1;",
		";@SUM(1)":    ";'@SUM(1)",
		"a;b;-c;\t=d": "a;b;'-c;'\t=d",
	} {
		if got := secevents.CSVCell(in); got != want {
			t.Errorf("CSVCell(%q) = %q, want %q", in, got, want)
		}
	}
}

func readCSV(t *testing.T, content []byte) [][]string {
	t.Helper()
	records, err := csv.NewReader(bytes.NewReader(content)).ReadAll()
	if err != nil {
		t.Fatalf("not CSV: %v\n%s", err, content)
	}
	return records
}

func TestWriteCSV_HasAHeaderAndOneRowPerEvent(t *testing.T) {
	uid := uint32(1001)
	events := []secevent.Event{
		{
			Node: node, OccurredAt: t0, ReceivedAt: t0.Add(time.Second), Kind: secevent.SSHLoginSucceeded, User: "=cmd|' /C calc'!A0",
			Remote:  &secevent.Remote{IP: netip.MustParseAddr("192.0.2.7"), Port: 50022},
			Details: secevent.Details{Method: "publickey", KeyType: "ED25519", KeyFingerprint: "SHA256:abc"}, NewSource: true,
		},
		{
			Node: node, OccurredAt: t0.Add(-time.Minute), ReceivedAt: t0, Kind: secevent.AccountCreated, User: "bob, \"the\" builder",
			Details: secevent.Details{UID: &uid}, Unverified: true,
		},
		{Node: node, OccurredAt: t0, ReceivedAt: t0, Kind: secevent.SudoCommand, User: "carol", Details: secevent.Details{Command: "-rf /"}},
	}
	var buf bytes.Buffer
	rows, truncated, err := secevents.WriteCSV(&buf, events, map[identity.NodeID]string{node: "@web-01"}, func(ip netip.Addr) string {
		if ip == netip.MustParseAddr("192.0.2.7") {
			return "SE"
		}
		return ""
	}, 1<<20)
	if err != nil || rows != 3 || truncated {
		t.Fatalf("WriteCSV() = %d, %v, %v", rows, truncated, err)
	}
	if !strings.Contains(buf.String(), "\r\n") {
		t.Error("lines end with CRLF")
	}
	records := readCSV(t, buf.Bytes())
	header := strings.Join(records[0], ",")
	if header != "occurred_at,received_at,node_id,hostname,kind,user,remote_ip,remote_host,remote_port,method,invalid_user,"+
		"key_type,key_fingerprint,tty,target,command,uid,pid,process,unverified,new_source,country" {
		t.Errorf("header = %s", header)
	}
	login := records[1]
	want := []string{
		"2026-11-20T12:00:00Z", "2026-11-20T12:00:01Z", node.String(), "'@web-01", "ssh.login.success", "'=cmd|' /C calc'!A0",
		"192.0.2.7", "", "50022", "publickey", "false", "ED25519", "SHA256:abc", "", "", "", "", "", "", "false", "true", "SE",
	}
	if strings.Join(login, "|") != strings.Join(want, "|") {
		t.Errorf("login row =\n%q\nwant\n%q", login, want)
	}
	if records[2][5] != "bob, \"the\" builder" || records[2][16] != "1001" || records[2][19] != "true" {
		t.Errorf("account row = %q", records[2])
	}
	if records[3][15] != "'-rf /" {
		t.Errorf("a command starting with - = %q", records[3][15])
	}
}

func TestWriteCSV_StopsAtTheSizeLimit(t *testing.T) {
	events := make([]secevent.Event, 50)
	for i := range events {
		events[i] = secevent.Event{Node: node, OccurredAt: t0, ReceivedAt: t0, Kind: secevent.SSHLoginFailed, User: strings.Repeat("u", 100)}
	}
	var buf bytes.Buffer
	rows, truncated, err := secevents.WriteCSV(&buf, events, nil, nil, 2000)
	if err != nil || !truncated || rows == 0 || rows >= 50 {
		t.Fatalf("WriteCSV() = %d rows, truncated %v, %v", rows, truncated, err)
	}
	if buf.Len() > 2000 {
		t.Errorf("%d bytes over the limit", buf.Len())
	}
	if got := len(readCSV(t, buf.Bytes())) - 1; got != rows {
		t.Errorf("%d rows in the file, %d reported", got, rows)
	}
}
