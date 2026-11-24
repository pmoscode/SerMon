package loki

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/pmoscode/SerMon/server/internal/app/ingest"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Writer stores the ingest's log entries (M3-07): one stream per label set, the rest as
// structured metadata.
type Writer struct{ client *Client }

var _ ingest.LogWriter = (*Writer)(nil)

// NewWriter returns the writer.
func NewWriter(client *Client) *Writer { return &Writer{client: client} }

var severities = [...]string{"unknown", "emergency", "alert", "critical", "error", "warning", "notice", "info", "debug"}

var sources = map[ingest.LogSource]string{ingest.LogSourceJournal: "journal", ingest.LogSourceFile: "file"}

// Write pushes the entries of the node; what Loki refuses for good is ingest.ErrLogsRejected.
func (w *Writer) Write(ctx context.Context, org tenant.OrgID, node identity.NodeID, entries []ingest.LogEntry) error {
	index := map[Labels]int{}
	var streams []Stream
	for _, e := range entries {
		severity := "unknown"
		if int(e.Severity) < len(severities) {
			severity = severities[e.Severity]
		}
		labels := Labels{Node: node, Source: sources[e.Source], Unit: e.Unit, Severity: severity}
		i, ok := index[labels]
		if !ok {
			i = len(streams)
			index[labels] = i
			streams = append(streams, Stream{Labels: labels})
		}
		streams[i].Entries = append(streams[i].Entries, Entry{At: e.At, Line: e.Message, Metadata: metadata(e)})
	}
	err := w.client.Push(ctx, org, streams)
	switch {
	case errors.Is(err, ErrRejected):
		return fmt.Errorf("%w: %w", ingest.ErrLogsRejected, err)
	case errors.Is(err, ErrRateLimited):
		return fmt.Errorf("%w: %w", ingest.ErrLogsThrottled, err)
	}
	return err
}

// metadata holds the fields beyond the labels; nil if there are none.
func metadata(e ingest.LogEntry) map[string]string {
	m := map[string]string{}
	set := func(key, value string) {
		if value != "" {
			m[key] = value
		}
	}
	set("identifier", e.Identifier)
	set("process", e.Process)
	set("boot_id", e.BootID)
	set("transport", e.Transport)
	set("file", e.File)
	if e.PID != 0 {
		m["pid"] = strconv.FormatUint(uint64(e.PID), 10)
	}
	if e.UID != nil {
		m["uid"] = strconv.FormatUint(uint64(*e.UID), 10)
	}
	if e.Facility != nil {
		m["facility"] = strconv.FormatUint(uint64(*e.Facility), 10)
	}
	if e.Truncated {
		m["truncated"] = "true"
	}
	if len(m) == 0 {
		return nil
	}
	return m
}
