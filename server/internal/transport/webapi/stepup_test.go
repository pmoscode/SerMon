package webapi_test

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	_ "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1" // register the web API descriptors
)

// mutatingProcedures are the web API procedures not marked NO_SIDE_EFFECTS.
func mutatingProcedures(t *testing.T) []string {
	t.Helper()
	var out []string
	protoregistry.GlobalFiles.RangeFilesByPackage("sermon.web.v1", func(fd protoreflect.FileDescriptor) bool {
		for i := range fd.Services().Len() {
			svc := fd.Services().Get(i)
			for j := range svc.Methods().Len() {
				m := svc.Methods().Get(j)
				opts, _ := m.Options().(*descriptorpb.MethodOptions)
				if opts.GetIdempotencyLevel() == descriptorpb.MethodOptions_NO_SIDE_EFFECTS {
					continue
				}
				out = append(out, "/"+string(svc.FullName())+"/"+string(m.Name()))
			}
		}
		return true
	})
	if len(out) == 0 {
		t.Fatal("no web API procedures found – registry not loaded")
	}
	return out
}

// M3-25: every procedure that changes something either needs a fresh step-up (and is tested
// with an aged one) or is exempt with a reason; a new RPC fails here until it is listed.
func TestStepUp_EveryProcedureIsListed(t *testing.T) {
	mutating := map[string]bool{}
	for _, p := range mutatingProcedures(t) {
		mutating[p] = true
		reason, required := stepUpRequired[p]
		why, exempt := stepUpExempt[p]
		switch {
		case required && exempt:
			t.Errorf("%s is both required and exempt – pick one", p)
		case !required && !exempt:
			t.Errorf("%s changes something: add it to stepUpRequired (with a call) or stepUpExempt (with a reason)", p)
		case required && reason == "", exempt && why == "":
			t.Errorf("%s: give a reason", p)
		}
		if !required {
			continue
		}
		_, own := stepUpOwnCases[p]
		c, isolated := tenantIsolationCases[p]
		if !own && !isolated {
			t.Errorf("%s has no call: add one to stepUpOwnCases", p)
		}
		if own && isolated && !c.list {
			t.Errorf("%s has a tenant-isolation case already: drop its stepUpOwnCases entry", p)
		}
	}
	for p := range stepUpRequired {
		if !mutating[p] {
			t.Errorf("%s in stepUpRequired is no mutating procedure", p)
		}
	}
	for p := range stepUpExempt {
		if !mutating[p] {
			t.Errorf("%s in stepUpExempt is no mutating procedure", p)
		}
	}
	for p := range stepUpOwnCases {
		if _, ok := stepUpRequired[p]; !ok {
			t.Errorf("%s has a call but is not required", p)
		}
	}
}
