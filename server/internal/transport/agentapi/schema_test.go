package agentapi_test

import (
	"testing"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/proto"
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
