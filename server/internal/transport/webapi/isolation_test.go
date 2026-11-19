package webapi_test

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	_ "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1" // register the web API descriptors
)

// tenantIsolation lists every web API procedure. Each one either has a cross-tenant test in
// tenantIsolationCases (integration tests: a user of Org B must get NOT_FOUND or PERMISSION_DENIED
// for Org A data) or a documented reason why it handles no tenant data.
var tenantIsolationExempt = map[string]string{
	"/sermon.web.v1.AuthService/Login":        "acts on the caller's own credentials, no tenant data",
	"/sermon.web.v1.AuthService/Logout":       "ends the caller's own session",
	"/sermon.web.v1.AuthService/GetSession":   "describes the caller's own session",
	"/sermon.web.v1.SystemService/GetVersion": "instance information, no tenant data (requires authentication)",
}

// TestTenantIsolation_EveryProcedureIsCovered fails when a new RPC is added without a
// cross-tenant test or an explicit exemption (KONZEPT §18.1, skill sermon-security-review).
func TestTenantIsolation_EveryProcedureIsCovered(t *testing.T) {
	var procedures []string
	protoregistry.GlobalFiles.RangeFilesByPackage("sermon.web.v1", func(fd protoreflect.FileDescriptor) bool {
		for i := range fd.Services().Len() {
			svc := fd.Services().Get(i)
			for j := range svc.Methods().Len() {
				procedures = append(procedures, "/"+string(svc.FullName())+"/"+string(svc.Methods().Get(j).Name()))
			}
		}
		return true
	})
	if len(procedures) == 0 {
		t.Fatal("no web API procedures found – registry not loaded")
	}
	for _, p := range procedures {
		_, exempt := tenantIsolationExempt[p]
		_, covered := tenantIsolationCases[p]
		switch {
		case exempt && covered:
			t.Errorf("%s is both exempt and covered – pick one", p)
		case !exempt && !covered:
			t.Errorf("%s has no tenant-isolation test (add it to tenantIsolationCases) and no exemption", p)
		}
		if reason := tenantIsolationExempt[p]; exempt && strings.TrimSpace(reason) == "" {
			t.Errorf("%s: exemption needs a reason", p)
		}
	}
}
