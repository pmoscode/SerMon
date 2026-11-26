package agentapi_test

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/agent/v1"
)

func validHostFacts() *agentv1.HostFacts {
	return &agentv1.HostFacts{
		Hostname:      "web-01",
		MachineId:     "0123456789abcdef0123456789abcdef",
		OsId:          "debian",
		OsVersion:     "13",
		KernelRelease: "6.12.0-1-amd64",
		Architecture:  agentv1.Architecture_ARCHITECTURE_AMD64,
		CpuCount:      4,
		MemoryBytes:   8 << 30,
	}
}

func hello(facts *agentv1.HostFacts) *agentv1.SessionRequest {
	return &agentv1.SessionRequest{Payload: &agentv1.SessionRequest_Hello{Hello: &agentv1.Hello{
		ProtocolVersion: 1,
		AgentVersion:    "0.0.0",
		HostFacts:       facts,
	}}}
}

func batch(samples ...*agentv1.Sample) *agentv1.SessionRequest {
	return &agentv1.SessionRequest{Payload: &agentv1.SessionRequest_Batch{Batch: &agentv1.Batch{
		Seq:     1_795_046_400_000_000,
		Content: &agentv1.Batch_Metrics{Metrics: &agentv1.MetricBatch{CollectedAt: timestamppb.Now(), Samples: samples}},
	}}}
}

func intSample(metric string, labels map[string]string) *agentv1.Sample {
	return &agentv1.Sample{Metric: metric, Labels: labels, Value: &agentv1.Sample_IntValue{IntValue: 1}}
}

func throttle(d time.Duration) *agentv1.SessionResponse {
	return &agentv1.SessionResponse{Payload: &agentv1.SessionResponse_Throttle{Throttle: &agentv1.Throttle{MinInterval: durationpb.New(d)}}}
}

func TestSchema_Batches(t *testing.T) {
	cpu := intSample("sermon_cpu_utilization_ratio", map[string]string{"cpu": "0", "mode": "user"})
	valid := map[string]proto.Message{
		"metrics":       batch(cpu, &agentv1.Sample{Metric: "sermon_load_1m", Value: &agentv1.Sample_DoubleValue{DoubleValue: 0.25}}),
		"long mount":    batch(intSample("sermon_filesystem_size_bytes", map[string]string{"mountpoint": "/" + strings.Repeat("a", 255)})),
		"throttle":      throttle(5 * time.Second),
		"throttle lift": throttle(0),
	}
	for name, msg := range valid {
		if err := protovalidate.Validate(msg); err != nil {
			t.Errorf("%s: Validate() = %v, want nil", name, err)
		}
	}

	tooMany := make([]*agentv1.Sample, 2001)
	for i := range tooMany {
		tooMany[i] = cpu
	}
	nineLabels := map[string]string{}
	for i := range 9 {
		nineLabels[fmt.Sprintf("l%d", i)] = "x"
	}
	invalid := map[string]proto.Message{
		"seq 0":              &agentv1.SessionRequest{Payload: &agentv1.SessionRequest_Batch{Batch: &agentv1.Batch{Content: batch(cpu).GetBatch().GetContent()}}},
		"no content":         &agentv1.SessionRequest{Payload: &agentv1.SessionRequest_Batch{Batch: &agentv1.Batch{Seq: 1}}},
		"no samples":         batch(),
		"too many samples":   batch(tooMany...),
		"no collection time": &agentv1.SessionRequest{Payload: &agentv1.SessionRequest_Batch{Batch: &agentv1.Batch{Seq: 1, Content: &agentv1.Batch_Metrics{Metrics: &agentv1.MetricBatch{Samples: []*agentv1.Sample{cpu}}}}}},
		"foreign metric":     batch(intSample("node_cpu_seconds_total", nil)),
		"metric injection":   batch(intSample("sermon_cpu\"} or 1", nil)),
		"upper case label":   batch(intSample("sermon_x", map[string]string{"CPU": "0"})),
		"too many labels":    batch(intSample("sermon_x", nineLabels)),
		"label value long":   batch(intSample("sermon_x", map[string]string{"mountpoint": strings.Repeat("a", 257)})),
		"no value":           batch(&agentv1.Sample{Metric: "sermon_x"}),
		"nan":                batch(&agentv1.Sample{Metric: "sermon_x", Value: &agentv1.Sample_DoubleValue{DoubleValue: math.NaN()}}),
		"infinity":           batch(&agentv1.Sample{Metric: "sermon_x", Value: &agentv1.Sample_DoubleValue{DoubleValue: math.Inf(1)}}),
		"throttle negative":  throttle(-time.Second),
		"throttle over 1 h":  throttle(time.Hour + time.Second),
	}
	for name, msg := range invalid {
		if err := protovalidate.Validate(msg); err == nil {
			t.Errorf("%s: Validate() = nil, want validation error", name)
		}
	}
}

func TestSchema_AcceptsValidMessages(t *testing.T) {
	valid := map[string]proto.Message{
		"hello":     hello(validHostFacts()),
		"heartbeat": &agentv1.SessionRequest{Payload: &agentv1.SessionRequest_Heartbeat{Heartbeat: &agentv1.Heartbeat{SentAt: timestamppb.Now()}}},
		"ack":       &agentv1.SessionResponse{Payload: &agentv1.SessionResponse_Ack{Ack: &agentv1.Ack{Seq: 42}}},
	}

	for name, msg := range valid {
		if err := protovalidate.Validate(msg); err != nil {
			t.Errorf("%s: Validate() = %v, want nil", name, err)
		}
	}
}

func TestSchema_RejectsInvalidMessages(t *testing.T) {
	withFacts := func(mutate func(*agentv1.HostFacts)) proto.Message {
		facts := validHostFacts()
		mutate(facts)
		return hello(facts)
	}

	invalid := map[string]proto.Message{
		"empty request":            &agentv1.SessionRequest{},
		"empty response":           &agentv1.SessionResponse{},
		"hello without host facts": hello(nil),
		"protocol version 0":       &agentv1.SessionRequest{Payload: &agentv1.SessionRequest_Hello{Hello: &agentv1.Hello{AgentVersion: "1", HostFacts: validHostFacts()}}},
		"heartbeat without time":   &agentv1.SessionRequest{Payload: &agentv1.SessionRequest_Heartbeat{Heartbeat: &agentv1.Heartbeat{}}},
		"agent version with a bidi": &agentv1.SessionRequest{Payload: &agentv1.SessionRequest_Hello{Hello: &agentv1.Hello{
			ProtocolVersion: 1, AgentVersion: "1.2.3\u202e", HostFacts: validHostFacts(),
		}}},
		"agent version no version": &agentv1.SessionRequest{Payload: &agentv1.SessionRequest_Hello{Hello: &agentv1.Hello{
			ProtocolVersion: 1, AgentVersion: "latest", HostFacts: validHostFacts(),
		}}},
		"empty hostname":           withFacts(func(f *agentv1.HostFacts) { f.Hostname = "" }),
		"hostname too long":        withFacts(func(f *agentv1.HostFacts) { f.Hostname = string(make([]byte, 254)) }),
		"machine id upper case":    withFacts(func(f *agentv1.HostFacts) { f.MachineId = "0123456789ABCDEF0123456789ABCDEF" }),
		"machine id too short":     withFacts(func(f *agentv1.HostFacts) { f.MachineId = "abc" }),
		"architecture unspecified": withFacts(func(f *agentv1.HostFacts) { f.Architecture = agentv1.Architecture_ARCHITECTURE_UNSPECIFIED }),
		"architecture unknown":     withFacts(func(f *agentv1.HostFacts) { f.Architecture = 99 }),
		"zero cpus":                withFacts(func(f *agentv1.HostFacts) { f.CpuCount = 0 }),
		"implausible cpu count":    withFacts(func(f *agentv1.HostFacts) { f.CpuCount = 100_000 }),
		"zero memory":              withFacts(func(f *agentv1.HostFacts) { f.MemoryBytes = 0 }),
		"kernel release too long":  withFacts(func(f *agentv1.HostFacts) { f.KernelRelease = string(make([]byte, 129)) }),
		"security source unknown":  withFacts(func(f *agentv1.HostFacts) { f.SecuritySource = 99 }),
	}

	for name, msg := range invalid {
		if err := protovalidate.Validate(msg); err == nil {
			t.Errorf("%s: Validate() = nil, want validation error", name)
		}
	}
}

func enrollRequest() *agentv1.EnrollRequest {
	return &agentv1.EnrollRequest{Token: "sme_x", Csr: []byte{0x30}, HostFacts: validHostFacts(), AgentVersion: "0.0.0"}
}

func TestSchema_EnrollRequest(t *testing.T) {
	if err := protovalidate.Validate(enrollRequest()); err != nil {
		t.Errorf("valid request: %v", err)
	}
	invalid := map[string]func(*agentv1.EnrollRequest){
		"no token":          func(r *agentv1.EnrollRequest) { r.Token = "" },
		"token too long":    func(r *agentv1.EnrollRequest) { r.Token = string(make([]byte, 65)) },
		"no csr":            func(r *agentv1.EnrollRequest) { r.Csr = nil },
		"csr too large":     func(r *agentv1.EnrollRequest) { r.Csr = make([]byte, 4097) },
		"no host facts":     func(r *agentv1.EnrollRequest) { r.HostFacts = nil },
		"invalid host fact": func(r *agentv1.EnrollRequest) { r.HostFacts.MachineId = "nope" },
		"no agent version":  func(r *agentv1.EnrollRequest) { r.AgentVersion = "" },
	}
	for name, mutate := range invalid {
		r := enrollRequest()
		mutate(r)
		if err := protovalidate.Validate(r); err == nil {
			t.Errorf("%s: Validate() = nil, want validation error", name)
		}
	}
}

func events(list ...*agentv1.SecurityEvent) *agentv1.SessionRequest {
	return &agentv1.SessionRequest{Payload: &agentv1.SessionRequest_Batch{Batch: &agentv1.Batch{
		Seq:     2,
		Content: &agentv1.Batch_SecurityEvents{SecurityEvents: &agentv1.SecurityEventBatch{Events: list}},
	}}}
}

func failedLogin(user string, remote *agentv1.SshRemote) *agentv1.SecurityEvent {
	return &agentv1.SecurityEvent{OccurredAt: timestamppb.Now(), Event: &agentv1.SecurityEvent_SshLoginFailed{SshLoginFailed: &agentv1.SshLoginFailed{
		User: user, Remote: remote, Method: agentv1.SshAuthMethod_SSH_AUTH_METHOD_PASSWORD,
	}}}
}

func fromIP(ip string, port uint32) *agentv1.SshRemote {
	return &agentv1.SshRemote{Address: &agentv1.SshRemote_Ip{Ip: ip}, Port: port}
}

// Security events (KONZEPT §11, M2-06): attacker-chosen fields are bounded, addresses are
// addresses.
func TestSchema_SecurityEvents(t *testing.T) {
	now := timestamppb.Now()
	valid := map[string]proto.Message{
		"failed login": events(failedLogin("alice", fromIP("192.0.2.7", 22))),
		"empty user":   events(failedLogin("", fromIP("2001:db8::1", 0))),
		"host name":    events(failedLogin("x", &agentv1.SshRemote{Address: &agentv1.SshRemote_Host{Host: "client.example.org"}, Port: 22})),
		"longest user": events(failedLogin(strings.Repeat("ä", 64), fromIP("192.0.2.7", 22))),
		"every kind": events(
			&agentv1.SecurityEvent{OccurredAt: now, Event: &agentv1.SecurityEvent_SshInvalidUser{SshInvalidUser: &agentv1.SshInvalidUser{User: "mallory", Remote: fromIP("192.0.2.7", 1)}}},
			&agentv1.SecurityEvent{OccurredAt: now, Event: &agentv1.SecurityEvent_SshLoginSucceeded{SshLoginSucceeded: &agentv1.SshLoginSucceeded{
				User: "alice", Remote: fromIP("::1", 2), Method: agentv1.SshAuthMethod_SSH_AUTH_METHOD_PUBLICKEY, KeyType: "ED25519", KeyFingerprint: "SHA256:abc",
			}}},
			&agentv1.SecurityEvent{OccurredAt: now, Event: &agentv1.SecurityEvent_SshPreauthDisconnect{SshPreauthDisconnect: &agentv1.SshPreauthDisconnect{Remote: fromIP("192.0.2.7", 3)}}},
			&agentv1.SecurityEvent{OccurredAt: now, Event: &agentv1.SecurityEvent_SudoAuthFailed{SudoAuthFailed: &agentv1.SudoAuthFailed{User: "alice", Tty: "pts/0"}}},
			&agentv1.SecurityEvent{OccurredAt: now, Event: &agentv1.SecurityEvent_SudoNotAllowed{SudoNotAllowed: &agentv1.SudoNotAllowed{User: "carol", Target: "root"}}},
			&agentv1.SecurityEvent{OccurredAt: now, Event: &agentv1.SecurityEvent_SudoCommand{SudoCommand: &agentv1.SudoCommand{User: "alice", Target: "root", Command: "/usr/bin/id"}}},
			&agentv1.SecurityEvent{OccurredAt: now, Event: &agentv1.SecurityEvent_AccountCreated{AccountCreated: &agentv1.AccountCreated{User: "bob", Uid: proto.Uint32(1001)}}},
			&agentv1.SecurityEvent{OccurredAt: now, Event: &agentv1.SecurityEvent_AccountDeleted{AccountDeleted: &agentv1.AccountDeleted{User: "bob"}}},
			&agentv1.SecurityEvent{OccurredAt: now, Event: &agentv1.SecurityEvent_SystemReboot{SystemReboot: &agentv1.SystemReboot{PreviousBootId: "cc8f81a486f84386b00bc31030b4e1bb", UptimeBefore: durationpb.New(time.Hour)}}},
			&agentv1.SecurityEvent{OccurredAt: now, Event: &agentv1.SecurityEvent_OomKill{OomKill: &agentv1.OomKill{Pid: 4242, Process: "python3"}}},
		),
	}
	for name, msg := range valid {
		if err := protovalidate.Validate(msg); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	many := make([]*agentv1.SecurityEvent, 501)
	for i := range many {
		many[i] = failedLogin("a", fromIP("192.0.2.7", 22))
	}
	invalid := map[string]proto.Message{
		"no events":       events(),
		"too many":        events(many...),
		"no time":         events(&agentv1.SecurityEvent{Event: failedLogin("a", fromIP("192.0.2.7", 22)).GetEvent()}),
		"no event":        events(&agentv1.SecurityEvent{OccurredAt: now}),
		"long user":       events(failedLogin(strings.Repeat("a", 129), fromIP("192.0.2.7", 22))),
		"not an ip":       events(failedLogin("a", fromIP("192.0.2.300", 22))),
		"bad host":        events(failedLogin("a", &agentv1.SshRemote{Address: &agentv1.SshRemote_Host{Host: "<script>"}})),
		"no address":      events(failedLogin("a", &agentv1.SshRemote{Port: 22})),
		"no remote":       events(failedLogin("a", nil)),
		"port":            events(failedLogin("a", fromIP("192.0.2.7", 65536))),
		"unknown method":  events(&agentv1.SecurityEvent{OccurredAt: now, Event: &agentv1.SecurityEvent_SshLoginFailed{SshLoginFailed: &agentv1.SshLoginFailed{User: "a", Remote: fromIP("192.0.2.7", 1), Method: 99}}}),
		"long command":    events(&agentv1.SecurityEvent{OccurredAt: now, Event: &agentv1.SecurityEvent_SudoCommand{SudoCommand: &agentv1.SudoCommand{User: "a", Target: "root", Command: strings.Repeat("x", 1025)}}}),
		"pid 0":           events(&agentv1.SecurityEvent{OccurredAt: now, Event: &agentv1.SecurityEvent_OomKill{OomKill: &agentv1.OomKill{Pid: 0, Process: "x"}}}),
		"negative uptime": events(&agentv1.SecurityEvent{OccurredAt: now, Event: &agentv1.SecurityEvent_SystemReboot{SystemReboot: &agentv1.SystemReboot{PreviousBootId: "a", UptimeBefore: durationpb.New(-time.Second)}}}),
	}
	for name, msg := range invalid {
		if err := protovalidate.Validate(msg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// What the agent makes of the fixture log lines (agent/crates/sermon-agent/src/security.rs writes
// proto/testdata/security_events.binpb) passes the server's validation, every kind of event.
func TestSchema_AgentSecurityEventsAreValid(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "proto", "testdata", "security_events.binpb"))
	if err != nil {
		t.Fatal(err)
	}
	var batch agentv1.SecurityEventBatch
	if err := proto.Unmarshal(raw, &batch); err != nil {
		t.Fatal(err)
	}
	if len(batch.GetEvents()) > 500 {
		batch.Events = batch.GetEvents()[:500]
	}
	if err := protovalidate.Validate(events(batch.GetEvents()...)); err != nil {
		t.Fatalf("the agent's events are invalid: %v", err)
	}
	kinds := map[string]bool{}
	for _, e := range batch.GetEvents() {
		kinds[fmt.Sprintf("%T", e.GetEvent())] = true
		// Written before ADR 0025: an older agent's events are verified (journald).
		if e.GetUnverified() {
			t.Errorf("golden event %v is unverified", e)
		}
	}
	if len(kinds) != 11 {
		t.Errorf("%d kinds of events in the golden batch, want all 11: %v", len(kinds), kinds)
	}
}

func logs(list ...*agentv1.LogEntry) *agentv1.SessionRequest {
	return &agentv1.SessionRequest{Payload: &agentv1.SessionRequest_Batch{Batch: &agentv1.Batch{
		Seq:     3,
		Content: &agentv1.Batch_Logs{Logs: &agentv1.LogBatch{Entries: list}},
	}}}
}

func journalEntry(message string) *agentv1.LogEntry {
	return &agentv1.LogEntry{
		OccurredAt: timestamppb.Now(), Source: agentv1.LogSource_LOG_SOURCE_JOURNAL,
		Unit: "nginx.service", Identifier: "nginx", Severity: agentv1.LogSeverity_LOG_SEVERITY_ERROR,
		Message: message, Pid: 812, Process: "nginx", Uid: proto.Uint32(33),
		BootId: "cc8f81a486f84386b00bc31030b4e1bb", Transport: "stdout", Facility: proto.Uint32(3),
	}
}

func withEntry(mutate func(*agentv1.LogEntry)) *agentv1.SessionRequest {
	e := journalEntry("upstream timed out")
	mutate(e)
	return logs(e)
}

// Log entries (KONZEPT §10.2, M3-01): every text is bounded, files are absolute paths.
func TestSchema_LogEntries(t *testing.T) {
	valid := map[string]proto.Message{
		"journal entry": logs(journalEntry("upstream timed out")),
		"minimal":       logs(&agentv1.LogEntry{OccurredAt: timestamppb.Now(), Source: agentv1.LogSource_LOG_SOURCE_JOURNAL}),
		"file entry": withEntry(func(e *agentv1.LogEntry) {
			e.Source, e.File, e.BootId, e.Transport = agentv1.LogSource_LOG_SOURCE_FILE, "/var/log/nginx/error.log", "", ""
		}),
		"container entry": withEntry(func(e *agentv1.LogEntry) {
			e.Source, e.Unit, e.Identifier, e.Transport, e.BootId = agentv1.LogSource_LOG_SOURCE_CONTAINER, "web", "web", "stderr", ""
		}),
		"longest message": logs(journalEntry(strings.Repeat("ä", 8192))),
		"markup and ansi": logs(journalEntry("<script>alert(1)</script> \x1b[31mred\x1b[0m")),
		"kernel facility": withEntry(func(e *agentv1.LogEntry) { e.Facility = proto.Uint32(0) }),
		"local7":          withEntry(func(e *agentv1.LogEntry) { e.Facility = proto.Uint32(23) }),
	}
	for name, msg := range valid {
		if err := protovalidate.Validate(msg); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	many := make([]*agentv1.LogEntry, 1001)
	for i := range many {
		many[i] = journalEntry("x")
	}
	invalid := map[string]proto.Message{
		"no entries":        logs(),
		"too many":          logs(many...),
		"no time":           withEntry(func(e *agentv1.LogEntry) { e.OccurredAt = nil }),
		"no source":         withEntry(func(e *agentv1.LogEntry) { e.Source = agentv1.LogSource_LOG_SOURCE_UNSPECIFIED }),
		"unknown source":    withEntry(func(e *agentv1.LogEntry) { e.Source = 9 }),
		"unknown severity":  withEntry(func(e *agentv1.LogEntry) { e.Severity = 9 }),
		"long message":      logs(journalEntry(strings.Repeat("x", 16385))),
		"long unit":         withEntry(func(e *agentv1.LogEntry) { e.Unit = strings.Repeat("u", 257) }),
		"long identifier":   withEntry(func(e *agentv1.LogEntry) { e.Identifier = strings.Repeat("i", 257) }),
		"long process":      withEntry(func(e *agentv1.LogEntry) { e.Process = strings.Repeat("p", 257) }),
		"file from journal": withEntry(func(e *agentv1.LogEntry) { e.File = "/var/log/syslog" }),
		"file without path": withEntry(func(e *agentv1.LogEntry) { e.Source = agentv1.LogSource_LOG_SOURCE_FILE }),
		"relative file":     withEntry(func(e *agentv1.LogEntry) { e.Source, e.File = agentv1.LogSource_LOG_SOURCE_FILE, "var/log/x" }),
		"long file": withEntry(func(e *agentv1.LogEntry) {
			e.Source, e.File = agentv1.LogSource_LOG_SOURCE_FILE, "/"+strings.Repeat("f", 4096)
		}),
		"boot id upper case":  withEntry(func(e *agentv1.LogEntry) { e.BootId = "CC8F81A486F84386B00BC31030B4E1BB" }),
		"boot id with dashes": withEntry(func(e *agentv1.LogEntry) { e.BootId = "cc8f81a4-86f8-4386-b00b-c31030b4e1bb" }),
		"transport injection": withEntry(func(e *agentv1.LogEntry) { e.Transport = `stdout"}` }),
		"facility 24":         withEntry(func(e *agentv1.LogEntry) { e.Facility = proto.Uint32(24) }),
	}
	for name, msg := range invalid {
		if err := protovalidate.Validate(msg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// What the agent makes of the fixture logs of every distro (agent/crates/sermon-agent/src/logs.rs
// writes proto/testdata/log_batch.binpb) passes the server's validation.
func TestSchema_AgentLogEntriesAreValid(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "proto", "testdata", "log_batch.binpb"))
	if err != nil {
		t.Fatal(err)
	}
	var batch agentv1.LogBatch
	if err := proto.Unmarshal(raw, &batch); err != nil {
		t.Fatal(err)
	}
	if err := protovalidate.Validate(logs(batch.GetEntries()...)); err != nil {
		t.Fatalf("the agent's log entries are invalid: %v", err)
	}
	sources, severities, truncated := map[agentv1.LogSource]bool{}, map[agentv1.LogSeverity]bool{}, false
	for _, e := range batch.GetEntries() {
		sources[e.GetSource()] = true
		severities[e.GetSeverity()] = true
		truncated = truncated || e.GetTruncated()
	}
	if len(sources) != 2 {
		t.Errorf("sources in the golden batch: %v, want journal and file", sources)
	}
	if len(severities) < 5 || !severities[agentv1.LogSeverity_LOG_SEVERITY_UNSPECIFIED] {
		t.Errorf("severities in the golden batch: %v, want several and unspecified", severities)
	}
	if !truncated {
		t.Error("no truncated entry in the golden batch")
	}
}

// The live view (M3-13): a snapshot within the protocol's bounds passes, every field over them
// fails – the server will relay snapshots to browsers (M3-14).
func TestSchema_ProcessSnapshots(t *testing.T) {
	v, err := protovalidate.New()
	if err != nil {
		t.Fatal(err)
	}
	uid, cpu, cmd := uint32(0), 0.5, "nginx: worker process"
	process := func(change func(*agentv1.ProcessInfo)) *agentv1.ProcessInfo {
		p := &agentv1.ProcessInfo{
			Pid: 812, Ppid: 1, Uid: &uid, User: "www-data", State: "S", CpuRatio: &cpu, Threads: 2,
			Name: "nginx", Cmdline: &cmd, Unit: "nginx.service", Container: "4f1c0a5e8b2d",
		}
		if change != nil {
			change(p)
		}
		return p
	}
	snapshot := func(processes ...*agentv1.ProcessInfo) *agentv1.SessionRequest {
		return &agentv1.SessionRequest{Payload: &agentv1.SessionRequest_ProcessSnapshot{ProcessSnapshot: &agentv1.ProcessSnapshot{
			TakenAt: timestamppb.Now(), Summary: &agentv1.SystemSummary{CpuCount: 4, Tasks: &agentv1.TaskCounts{Total: 1}},
			Processes: processes,
		}}}
	}
	if err := v.Validate(snapshot(process(nil))); err != nil {
		t.Errorf("valid snapshot: %v", err)
	}
	long := strings.Repeat("x", 1025)
	tooBusy, negative := 4097.0, -0.1
	for name, p := range map[string]*agentv1.ProcessInfo{
		"pid 0":           process(func(p *agentv1.ProcessInfo) { p.Pid = 0 }),
		"no state":        process(func(p *agentv1.ProcessInfo) { p.State = "" }),
		"long cmdline":    process(func(p *agentv1.ProcessInfo) { p.Cmdline = &long }),
		"long name":       process(func(p *agentv1.ProcessInfo) { p.Name = long[:65] }),
		"cpu over":        process(func(p *agentv1.ProcessInfo) { p.CpuRatio = &tooBusy }),
		"cpu negative":    process(func(p *agentv1.ProcessInfo) { p.CpuRatio = &negative }),
		"upper container": process(func(p *agentv1.ProcessInfo) { p.Container = "4F1C0A5E8B2D" }),
		"long container":  process(func(p *agentv1.ProcessInfo) { p.Container = "4f1c0a5e8b2d0" }),
	} {
		if err := v.Validate(snapshot(p)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	many := make([]*agentv1.ProcessInfo, 4001)
	for i := range many {
		many[i] = process(nil)
	}
	if err := v.Validate(snapshot(many...)); err == nil {
		t.Error("4001 processes accepted")
	}
	noTime := snapshot(process(nil))
	noTime.GetProcessSnapshot().TakenAt = nil
	if err := v.Validate(noTime); err == nil {
		t.Error("a snapshot without time accepted")
	}
	start := func(d time.Duration) *agentv1.SessionResponse {
		return &agentv1.SessionResponse{Payload: &agentv1.SessionResponse_StartLiveView{StartLiveView: &agentv1.StartLiveView{Interval: durationpb.New(d)}}}
	}
	if err := v.Validate(start(2 * time.Second)); err != nil {
		t.Errorf("2 s: %v", err)
	}
	for _, d := range []time.Duration{500 * time.Millisecond, 11 * time.Second} {
		if err := v.Validate(start(d)); err == nil {
			t.Errorf("interval %v accepted", d)
		}
	}
	if err := v.Validate(&agentv1.SessionRequest{Payload: &agentv1.SessionRequest_LiveViewRefused{LiveViewRefused: &agentv1.LiveViewRefused{Reason: long[:257]}}}); err == nil {
		t.Error("a long refusal accepted")
	}
}

func TestSchema_AgentSnapshotIsValid(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "proto", "testdata", "process_snapshot.binpb"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot agentv1.ProcessSnapshot
	if err := proto.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	req := &agentv1.SessionRequest{Payload: &agentv1.SessionRequest_ProcessSnapshot{ProcessSnapshot: &snapshot}}
	if err := protovalidate.Validate(req); err != nil {
		t.Fatalf("the agent's snapshot is invalid: %v", err)
	}
	var odd, kernel, zombie bool
	for _, p := range snapshot.GetProcesses() {
		odd = odd || p.GetName() == "evil) name (x"
		kernel = kernel || p.GetKernelThread()
		zombie = zombie || p.GetState() == "Z"
	}
	if len(snapshot.GetProcesses()) != 9 || !odd || !kernel || !zombie {
		t.Errorf("golden snapshot: %d processes, odd %v, kernel %v, zombie %v", len(snapshot.GetProcesses()), odd, kernel, zombie)
	}
}

// Updates and the commands of the root helper (M4-05): the agent's reports and results within
// the bounds pass, the server's commands only with a lowercase UUID, a known scope and a reason
// without control characters.
func TestSchema_UpdatesAndCommands(t *testing.T) {
	v, err := protovalidate.New()
	if err != nil {
		t.Fatal(err)
	}
	const runID = "0193f5a0-0000-7000-8000-000000000001"
	report := func(change func(*agentv1.UpdateReport)) proto.Message {
		r := &agentv1.UpdateReport{
			CollectedAt: timestamppb.Now(), Availability: agentv1.UpdateAvailability_UPDATE_AVAILABILITY_LISTED,
			PackageManager: agentv1.PackageManager_PACKAGE_MANAGER_APT,
			Packages: []*agentv1.UpdatePackage{{
				Name: "openssl", InstalledVersion: "3.0.15-1~deb12u1", CandidateVersion: "3.0.16-1~deb12u1",
				Source: "bookworm-security", Security: true,
			}},
			TotalCount: 1, SecurityCount: 1, RebootRequired: true, RebootReason: "linux-image-6.1.0-28-amd64",
			Permissions: &agentv1.HelperPermissions{RemoteUpdates: true},
		}
		if change != nil {
			change(r)
		}
		return &agentv1.SessionRequest{Payload: &agentv1.SessionRequest_Batch{Batch: &agentv1.Batch{
			Seq: 1, Content: &agentv1.Batch_UpdateReport{UpdateReport: r},
		}}}
	}
	result := func(change func(*agentv1.CommandResult)) proto.Message {
		r := &agentv1.CommandResult{
			RunId: runID, Phase: agentv1.CommandPhase_COMMAND_PHASE_FAILED, ExitCode: 100, Reason: "dpkg was interrupted",
			Output: "E: dpkg was interrupted", StartedAt: timestamppb.Now(), EndedAt: timestamppb.Now(),
		}
		if change != nil {
			change(r)
		}
		return &agentv1.SessionRequest{Payload: &agentv1.SessionRequest_Batch{Batch: &agentv1.Batch{
			Seq: 2, Content: &agentv1.Batch_CommandResult{CommandResult: r},
		}}}
	}
	command := func(payload any) proto.Message {
		res := &agentv1.SessionResponse{}
		switch p := payload.(type) {
		case *agentv1.RunUpdate:
			res.Payload = &agentv1.SessionResponse_RunUpdate{RunUpdate: p}
		case *agentv1.RebootNode:
			res.Payload = &agentv1.SessionResponse_RebootNode{RebootNode: p}
		case *agentv1.SelfUpdate:
			res.Payload = &agentv1.SessionResponse_SelfUpdate{SelfUpdate: p}
		}
		return res
	}
	security := agentv1.UpdateScope_UPDATE_SCOPE_SECURITY
	valid := map[string]proto.Message{
		"report": report(nil),
		"no helper": report(func(r *agentv1.UpdateReport) {
			r.Availability, r.Packages, r.Permissions = agentv1.UpdateAvailability_UPDATE_AVAILABILITY_NO_HELPER, nil, nil
		}),
		"alpine": report(func(r *agentv1.UpdateReport) {
			r.PackageManager, r.SecurityCount = agentv1.PackageManager_PACKAGE_MANAGER_APK, 0
		}),
		"result": result(nil),
		"started": result(func(r *agentv1.CommandResult) {
			r.Phase, r.ExitCode, r.Output, r.EndedAt = agentv1.CommandPhase_COMMAND_PHASE_STARTED, 0, "", nil
		}),
		"full output":  result(func(r *agentv1.CommandResult) { r.Output = strings.Repeat("x", 256<<10) }),
		"refresh":      &agentv1.SessionResponse{Payload: &agentv1.SessionResponse_RefreshUpdateInfo{RefreshUpdateInfo: &agentv1.RefreshUpdateInfo{}}},
		"run update":   command(&agentv1.RunUpdate{RunId: runID, Scope: security}),
		"reboot":       command(&agentv1.RebootNode{RunId: runID, Reason: "updates (SerMon) – ändern"}),
		"self update":  command(&agentv1.SelfUpdate{RunId: runID, Version: "0.5.0"}),
		"beta version": command(&agentv1.SelfUpdate{RunId: runID, Version: "0.5.0-beta.1"}),
	}
	for name, msg := range valid {
		if err := v.Validate(msg); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	many := make([]*agentv1.UpdatePackage, 5001)
	for i := range many {
		many[i] = &agentv1.UpdatePackage{Name: "p"}
	}
	long := strings.Repeat("x", 257)
	invalid := map[string]proto.Message{
		"report without time":    report(func(r *agentv1.UpdateReport) { r.CollectedAt = nil }),
		"no availability":        report(func(r *agentv1.UpdateReport) { r.Availability = 0 }),
		"unknown availability":   report(func(r *agentv1.UpdateReport) { r.Availability = 99 }),
		"unknown manager":        report(func(r *agentv1.UpdateReport) { r.PackageManager = 99 }),
		"too many packages":      report(func(r *agentv1.UpdateReport) { r.Packages = many }),
		"package without name":   report(func(r *agentv1.UpdateReport) { r.Packages[0].Name = "" }),
		"long package name":      report(func(r *agentv1.UpdateReport) { r.Packages[0].Name = long }),
		"long version":           report(func(r *agentv1.UpdateReport) { r.Packages[0].CandidateVersion = long }),
		"long source":            report(func(r *agentv1.UpdateReport) { r.Packages[0].Source = long }),
		"long reboot reason":     report(func(r *agentv1.UpdateReport) { r.RebootReason = strings.Repeat("x", 1025) }),
		"implausible count":      report(func(r *agentv1.UpdateReport) { r.TotalCount = 1_000_001 }),
		"result without run":     result(func(r *agentv1.CommandResult) { r.RunId = "" }),
		"upper case run":         result(func(r *agentv1.CommandResult) { r.RunId = strings.ToUpper(runID) }),
		"no phase":               result(func(r *agentv1.CommandResult) { r.Phase = 0 }),
		"unknown phase":          result(func(r *agentv1.CommandResult) { r.Phase = 99 }),
		"output too long":        result(func(r *agentv1.CommandResult) { r.Output = strings.Repeat("x", 256<<10+1) }),
		"long reason":            result(func(r *agentv1.CommandResult) { r.Reason = strings.Repeat("x", 1025) }),
		"update without run":     command(&agentv1.RunUpdate{Scope: security}),
		"path as run":            command(&agentv1.RunUpdate{RunId: "../../etc/passwd", Scope: security}),
		"no scope":               command(&agentv1.RunUpdate{RunId: runID}),
		"unknown scope":          command(&agentv1.RunUpdate{RunId: runID, Scope: 7}),
		"reboot without reason":  command(&agentv1.RebootNode{RunId: runID}),
		"reboot reason newline":  command(&agentv1.RebootNode{RunId: runID, Reason: "a\nb"}),
		"reboot reason escape":   command(&agentv1.RebootNode{RunId: runID, Reason: "\x1b[2J"}),
		"long reboot":            command(&agentv1.RebootNode{RunId: runID, Reason: strings.Repeat("x", 201)}),
		"reboot without run":     command(&agentv1.RebootNode{Reason: "x"}),
		"self update no version": command(&agentv1.SelfUpdate{RunId: runID}),
		"self update odd":        command(&agentv1.SelfUpdate{RunId: runID, Version: "latest; rm"}),
		"self update no run":     command(&agentv1.SelfUpdate{Version: "0.5.0"}),
	}
	for name, msg := range invalid {
		if err := v.Validate(msg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// M5-02 (ADR 0039): the messages of the modules – configuration, credentials, health and hints –
// with their bounds.
func TestSchema_Modules(t *testing.T) {
	config := func(modules ...*agentv1.ModuleConfig) proto.Message {
		return &agentv1.SessionResponse{Payload: &agentv1.SessionResponse_ConfigUpdate{ConfigUpdate: &agentv1.ConfigUpdate{
			Version: 1, Config: &agentv1.AgentConfig{Logs: &agentv1.LogConfig{}, Modules: modules},
		}}}
	}
	nginx := func(mutate func(*agentv1.ModuleConfig)) *agentv1.ModuleConfig {
		m := &agentv1.ModuleConfig{Id: "nginx", Enabled: true, Interval: durationpb.New(30 * time.Second), ConfigJson: `{"url":"http://127.0.0.1/stub_status"}`}
		if mutate != nil {
			mutate(m)
		}
		return m
	}
	secrets := func(s ...*agentv1.ModuleSecret) proto.Message {
		return &agentv1.SessionResponse{Payload: &agentv1.SessionResponse_ModuleSecrets{ModuleSecrets: &agentv1.ModuleSecrets{Secrets: s}}}
	}
	content := func(b *agentv1.Batch) proto.Message {
		b.Seq = 1
		return &agentv1.SessionRequest{Payload: &agentv1.SessionRequest_Batch{Batch: b}}
	}
	health := func(h ...*agentv1.ModuleHealth) proto.Message {
		return content(&agentv1.Batch{Content: &agentv1.Batch_ModuleHealth{ModuleHealth: &agentv1.ModuleHealthReport{CollectedAt: timestamppb.Now(), Modules: h}}})
	}
	hints := func(h ...*agentv1.DiscoveryHint) proto.Message {
		return content(&agentv1.Batch{Content: &agentv1.Batch_DiscoveryHints{DiscoveryHints: &agentv1.DiscoveryHints{CollectedAt: timestamppb.Now(), Hints: h}}})
	}
	withModules := func(ids ...string) proto.Message {
		h := hello(validHostFacts())
		h.GetHello().Modules = ids
		return h
	}
	tooManyHints := make([]*agentv1.DiscoveryHint, 33)
	for i := range tooManyHints {
		tooManyHints[i] = &agentv1.DiscoveryHint{ModuleId: "nginx", Reasons: []string{"port:80"}}
	}
	valid := map[string]proto.Message{
		"config":            config(nginx(nil), nginx(func(m *agentv1.ModuleConfig) { m.Id, m.Interval = "postgresql", nil })),
		"config off":        config(nginx(func(m *agentv1.ModuleConfig) { m.Enabled = false })),
		"secret":            secrets(&agentv1.ModuleSecret{ModuleId: "postgresql", Field: "password", Value: "s3cret"}),
		"health":            health(&agentv1.ModuleHealth{ModuleId: "nginx", State: agentv1.ModuleState_MODULE_STATE_OK, LastSuccess: timestamppb.Now()}),
		"health empty":      health(),
		"hints":             hints(&agentv1.DiscoveryHint{ModuleId: "nginx", Reasons: []string{"process:nginx", "port:80", "unit:nginx.service"}, SuggestedConfigJson: `{"port":80}`}),
		"hello with module": withModules("nginx", "http_check"),
	}
	for name, msg := range valid {
		if err := protovalidate.Validate(msg); err != nil {
			t.Errorf("%s: Validate() = %v, want nil", name, err)
		}
	}
	invalid := map[string]proto.Message{
		"module twice":       config(nginx(nil), nginx(nil)),
		"module id upper":    config(nginx(func(m *agentv1.ModuleConfig) { m.Id = "Nginx" })),
		"module id path":     config(nginx(func(m *agentv1.ModuleConfig) { m.Id = "../x" })),
		"interval 5 s":       config(nginx(func(m *agentv1.ModuleConfig) { m.Interval = durationpb.New(5 * time.Second) })),
		"interval 2 h":       config(nginx(func(m *agentv1.ModuleConfig) { m.Interval = durationpb.New(2 * time.Hour) })),
		"config too large":   config(nginx(func(m *agentv1.ModuleConfig) { m.ConfigJson = strings.Repeat("x", 16385) })),
		"secret empty":       secrets(&agentv1.ModuleSecret{ModuleId: "postgresql", Field: "password"}),
		"secret field":       secrets(&agentv1.ModuleSecret{ModuleId: "postgresql", Field: "Pass word", Value: "x"}),
		"secret too long":    secrets(&agentv1.ModuleSecret{ModuleId: "postgresql", Field: "password", Value: strings.Repeat("x", 4097)}),
		"health unspecified": health(&agentv1.ModuleHealth{ModuleId: "nginx"}),
		"health reason long": health(&agentv1.ModuleHealth{ModuleId: "nginx", State: agentv1.ModuleState_MODULE_STATE_ERROR, Reason: strings.Repeat("x", 257)}),
		"health no time":     content(&agentv1.Batch{Content: &agentv1.Batch_ModuleHealth{ModuleHealth: &agentv1.ModuleHealthReport{}}}),
		"hint no reason":     hints(&agentv1.DiscoveryHint{ModuleId: "nginx"}),
		"hint raw data":      hints(&agentv1.DiscoveryHint{ModuleId: "nginx", Reasons: []string{"cmdline:nginx -g daemon off"}}),
		"hint bidi":          hints(&agentv1.DiscoveryHint{ModuleId: "nginx", Reasons: []string{"process:ngin\u202ex"}}),
		"too many hints":     hints(tooManyHints...),
		"hello module twice": withModules("nginx", "nginx"),
	}
	for name, msg := range invalid {
		if err := protovalidate.Validate(msg); err == nil {
			t.Errorf("%s: Validate() = nil, want validation error", name)
		}
	}
}
