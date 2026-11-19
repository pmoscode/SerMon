package agentapi_test

import (
	"fmt"
	"math"
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
