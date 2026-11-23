//go:build integration

package webapi_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pmoscode/SerMon/server/internal/app/ingest"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
)

// The log explorer over the real stack and Loki (M3-08): members with view_logs read the lines of
// their nodes with histogram and context; another organization reads nothing.
func TestLogs_MembersReadTheLinesOfTheirNodes(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	seedLogAlpha(t)
	ctx := context.Background()
	filter := &webv1.LogFilter{
		From: timestamppb.New(time.Now().Add(-time.Hour)), To: timestamppb.New(time.Now().Add(time.Minute)),
		Text: "SECRET", IgnoreCase: true, MinSeverity: webv1.LogSeverity_LOG_SEVERITY_ERROR,
	}
	for _, email := range []string{"ann@example.org", "mia@example.org"} { // admin, viewer of the group
		member := login(t, s, email)
		var lines []*webv1.LogLine
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			res, err := member.logs.QueryLogs(ctx, connect.NewRequest(&webv1.QueryLogsRequest{Filter: filter}))
			if err != nil {
				t.Fatalf("%s: %v", email, err)
			}
			if lines = res.Msg.GetLines(); len(lines) > 0 {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		// seedLogAlpha writes one line per test of the binary.
		if len(lines) == 0 || lines[0].GetMessage() != "alpha secret line" || lines[0].GetNodeId() != nodeAl || len(lines[0].GetFields()) != 0 ||
			lines[0].GetHostname() != "alpha-01" || lines[0].GetSeverity() != webv1.LogSeverity_LOG_SEVERITY_ERROR ||
			lines[0].GetUnit() != "nginx.service" || lines[0].GetSource() != webv1.LogSource_LOG_SOURCE_JOURNAL {
			t.Fatalf("%s: lines = %v", email, lines)
		}
		hist, err := member.logs.GetLogHistogram(ctx, connect.NewRequest(&webv1.GetLogHistogramRequest{Filter: filter}))
		if err != nil || len(hist.Msg.GetSeries()) != 1 || hist.Msg.GetSeries()[0].GetSeverity() != webv1.LogSeverity_LOG_SEVERITY_ERROR {
			t.Errorf("%s: histogram = %v, %v", email, hist, err)
		}
		around, err := member.logs.GetLogContext(ctx, connect.NewRequest(&webv1.GetLogContextRequest{
			NodeId: nodeAl, At: lines[0].GetAt(), Source: webv1.LogSource_LOG_SOURCE_JOURNAL, Unit: "nginx.service",
		}))
		if err != nil || len(around.Msg.GetLines()) == 0 {
			t.Errorf("%s: context = %v, %v", email, around, err)
		}
	}
	bea := login(t, s, "bea@example.org")
	res, err := bea.logs.QueryLogs(ctx, connect.NewRequest(&webv1.QueryLogsRequest{Filter: filter}))
	if err != nil || len(res.Msg.GetLines()) != 0 {
		t.Errorf("Beta: %v, %v", res, err)
	}
	bad := &webv1.LogFilter{From: timestamppb.New(time.Now().Add(-30 * 24 * time.Hour)), To: timestamppb.Now()}
	if _, err := bea.logs.QueryLogs(ctx, connect.NewRequest(&webv1.QueryLogsRequest{Filter: bad})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("30 days: %v, want INVALID_ARGUMENT", err)
	}
}

// The tail over the real stack and Loki (M3-09): a line written after the start arrives exactly
// once, with its fields; Beta's tail of the same time sees nothing.
func TestTailLogs_ALineArrivesExactlyOnce(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	open := func(member loggedIn, email string) *connect.ServerStreamForClient[webv1.TailLogsResponse] {
		t.Helper()
		stream, err := member.logs.TailLogs(ctx, connect.NewRequest(&webv1.TailLogsRequest{Text: "tailed", Units: []string{"cron.service"}}))
		if err != nil || !stream.Receive() {
			t.Fatalf("%s: %v %v", email, err, stream.Err())
		}
		return stream
	}
	mia, bea := open(login(t, s, "mia@example.org"), "mia"), open(login(t, s, "bea@example.org"), "bea")
	defer func() { _, _ = mia.Close(), bea.Close() }()
	message := "tailed " + time.Now().Format(time.RFC3339Nano)
	writeLogAlpha(t, ingest.LogEntry{At: time.Now(), Source: ingest.LogSourceJournal, Unit: "cron.service", Severity: 7, Message: message})

	// The line comes within a few rounds; then nothing more of it for 40 s (beyond the window).
	got := 0
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) && mia.Receive() {
		for _, l := range mia.Msg().GetLines() {
			if l.GetMessage() != message || l.GetHostname() != "alpha-01" || l.GetSeverity() != webv1.LogSeverity_LOG_SEVERITY_INFO {
				t.Errorf("line = %v", l)
			}
			got++
		}
	}
	if got != 1 {
		t.Errorf("the line arrived %d times, want once (%v)", got, mia.Err())
	}
	_ = bea.Close()
	for bea.Receive() {
		if len(bea.Msg().GetLines()) > 0 {
			t.Errorf("Beta's tail got %v", bea.Msg().GetLines())
		}
	}
}
