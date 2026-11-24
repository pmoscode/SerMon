package loki_test

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/adapters/loki"
	"github.com/pmoscode/SerMon/server/internal/app/ingest"
	"github.com/pmoscode/SerMon/server/internal/app/logs"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/logql"
)

func writtenStreams(t *testing.T, body []byte) pushBody {
	t.Helper()
	zr, err := gzip.NewReader(strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	var out pushBody
	if err := json.NewDecoder(zr).Decode(&out); err != nil {
		t.Fatal(err)
	}
	sort.Slice(out.Streams, func(i, j int) bool { return out.Streams[i].Stream["unit"] < out.Streams[j].Stream["unit"] })
	return out
}

// The ingest's entries (M3-07) become one stream per label set; the rest is metadata.
func TestWriter_GroupsEntriesIntoStreamsWithMetadata(t *testing.T) {
	rec := &recorded{}
	w := loki.NewWriter(client(t, rec.handler(http.StatusNoContent, "")))
	uid, facility := uint32(0), uint8(4)
	entries := []ingest.LogEntry{
		{
			At: at, Source: ingest.LogSourceJournal, Unit: "ssh.service", Severity: 7, Message: "Accepted publickey", Identifier: "sshd-session",
			PID: 23, Process: "sshd-session", UID: &uid, BootID: "cc8f81a486f84386b00bc31030b4e1bb", Transport: "syslog", Facility: &facility,
		},
		{At: at, Source: ingest.LogSourceJournal, Unit: "ssh.service", Severity: 7, Message: "Disconnected"},
		{At: at, Source: ingest.LogSourceFile, Unit: "nginx", Severity: 0, Message: "x", File: "/var/log/nginx/error.log", Truncated: true},
	}
	if err := w.Write(context.Background(), org, node, entries); err != nil {
		t.Fatal(err)
	}
	body := writtenStreams(t, rec.bodies[0])
	if len(body.Streams) != 2 {
		t.Fatalf("%d streams, want one per label set", len(body.Streams))
	}
	nginx, ssh := body.Streams[0], body.Streams[1]
	if ssh.Stream["source"] != "journal" || ssh.Stream["severity"] != "info" || len(ssh.Values) != 2 || ssh.Stream["node_id"] != node.String() {
		t.Errorf("ssh stream = %+v", ssh)
	}
	meta, _ := ssh.Values[0][2].(map[string]any)
	want := map[string]any{
		"identifier": "sshd-session", "pid": "23", "process": "sshd-session", "uid": "0",
		"boot_id": "cc8f81a486f84386b00bc31030b4e1bb", "transport": "syslog", "facility": "4",
	}
	for k, v := range want {
		if meta[k] != v {
			t.Errorf("metadata %s = %v, want %v (%v)", k, meta[k], v, meta)
		}
	}
	if len(ssh.Values[1]) != 2 {
		t.Errorf("an entry without metadata carries none: %v", ssh.Values[1])
	}
	if nginx.Stream["source"] != "file" || nginx.Stream["severity"] != "unknown" {
		t.Errorf("file stream = %v", nginx.Stream)
	}
	if fm, _ := nginx.Values[0][2].(map[string]any); fm["file"] != "/var/log/nginx/error.log" || fm["truncated"] != "true" {
		t.Errorf("file metadata = %v", nginx.Values[0])
	}
}

func TestWriter_SeverityNames(t *testing.T) {
	rec := &recorded{}
	w := loki.NewWriter(client(t, rec.handler(http.StatusNoContent, "")))
	names := []string{"unknown", "emergency", "alert", "critical", "error", "warning", "notice", "info", "debug"}
	for code, name := range names {
		e := ingest.LogEntry{At: at, Source: ingest.LogSourceJournal, Severity: uint8(code), Message: name}
		if err := w.Write(context.Background(), org, node, []ingest.LogEntry{e}); err != nil {
			t.Fatal(err)
		}
		if got := writtenStreams(t, rec.bodies[code]).Streams[0].Stream["severity"]; got != name {
			t.Errorf("severity %d = %s, want %s", code, got, name)
		}
	}
}

// What Loki refuses for good is ErrLogsRejected for the ingest (dropped); its limits of the
// tenant are ErrLogsThrottled (retried, then dropped for the organization only); an outage is
// neither.
func TestWriter_MapsTheErrorsForTheIngest(t *testing.T) {
	entry := []ingest.LogEntry{{At: at, Source: ingest.LogSourceJournal, Message: "x"}}
	for status, want := range map[int][2]bool{
		http.StatusBadRequest: {true, false}, http.StatusTooManyRequests: {false, true}, http.StatusServiceUnavailable: {false, false},
	} {
		w := loki.NewWriter(client(t, (&recorded{}).handler(status, "nope")))
		err := w.Write(context.Background(), org, node, entry)
		if err == nil || errors.Is(err, ingest.ErrLogsRejected) != want[0] || errors.Is(err, ingest.ErrLogsThrottled) != want[1] {
			t.Errorf("%d: %v, want rejected %v, throttled %v", status, err, want[0], want[1])
		}
	}
}

func TestReader_MapsLinesAndAnOutage(t *testing.T) {
	response := `{"status":"success","data":{"resultType":"streams","result":[
		{"stream":{"node_id":"` + node.String() + `","unit":"nginx.service"},"values":[["1795435200123456789","x",{"structuredMetadata":{"pid":"1"}}]]}]}}`
	r := loki.NewReader(client(t, (&recorded{}).handler(http.StatusOK, response)))
	q, _ := logql.Select([]identity.NodeID{node})
	lines, err := r.Lines(context.Background(), org, q, at.Add(-time.Hour), at, 10, true)
	if err != nil || len(lines) != 1 || lines[0].Metadata["pid"] != "1" || lines[0].Labels["unit"] != "nginx.service" {
		t.Errorf("Lines() = %+v, %v", lines, err)
	}
	down := loki.NewReader(client(t, (&recorded{}).handler(http.StatusServiceUnavailable, "")))
	if _, err := down.Lines(context.Background(), org, q, at.Add(-time.Hour), at, 10, true); !errors.Is(err, logs.ErrUnavailable) {
		t.Errorf("Loki down: %v", err)
	}
}
