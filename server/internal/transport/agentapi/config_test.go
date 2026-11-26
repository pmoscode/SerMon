package agentapi_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/logconfig"
	"github.com/pmoscode/SerMon/server/internal/domain/module"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	agentv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/agent/v1"
	"github.com/pmoscode/SerMon/server/internal/secrets"
	"github.com/pmoscode/SerMon/server/internal/transport/agentapi"
)

// fakeConfigs holds one configuration per organization and records the agents' answers.
type fakeConfigs struct {
	mu       sync.Mutex
	configs  map[tenant.OrgID]logconfig.NodeConfig
	asked    []identity.NodeID
	answered []answer
	// announced are the modules of each Hello; changed is what Announced reports.
	announced [][]string
	changed   bool
}

type answer struct {
	org     tenant.OrgID
	node    identity.NodeID
	applied logconfig.Applied
}

func (f *fakeConfigs) set(org tenant.OrgID, c logconfig.NodeConfig) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.configs[org] = c
}

func (f *fakeConfigs) ForNode(_ context.Context, org tenant.OrgID, node identity.NodeID) (logconfig.NodeConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, node)
	return f.configs[org], nil
}

func (f *fakeConfigs) Applied(_ context.Context, org tenant.OrgID, node identity.NodeID, a logconfig.Applied) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answered = append(f.answered, answer{org, node, a})
	return nil
}

func (f *fakeConfigs) Announced(_ context.Context, _ tenant.OrgID, _ identity.NodeID, modules []string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.announced = append(f.announced, modules)
	return f.changed, nil
}

func (f *fakeConfigs) answers() []answer {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]answer(nil), f.answered...)
}

func helloWithConfig(version uint64) *agentv1.SessionRequest {
	msg := helloMsg()
	msg.GetHello().ConfigVersion = version
	return msg
}

func nodeConfig(version uint64) logconfig.NodeConfig {
	return logconfig.NodeConfig{
		Version: version,
		Settings: logconfig.Settings{
			Enabled: true, MinSeverity: logconfig.SeverityWarning, ExcludeUnits: []string{"cron.service"},
			Files: []logconfig.FileGlob{{Pattern: "/var/log/nginx/*.log", MultilineStart: `^\d`}},
		},
		Rules: []logconfig.Rule{{Pattern: `customer-\d{6}`}},
	}
}

// within bounds a stream, so that a missing message fails the test instead of hanging it.
func within(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func startConfigServer(t *testing.T, configs *fakeConfigs) server {
	t.Helper()
	return startServerFull(t, newTestCA(t), 0, nil, []agentapi.HandlerOption{agentapi.WithAgentConfig(configs)})
}

// ADR 0030: a configuration that differs from the agent's version is sent right after the trust
// bundle, with every field; the agent's answer is stored under the certificate's identity.
func TestConfig_SentAtTheStartWhenTheVersionDiffersAndTheAnswerIsStored(t *testing.T) {
	configs := &fakeConfigs{configs: map[tenant.OrgID]logconfig.NodeConfig{orgX: nodeConfig(3)}}
	s := startConfigServer(t, configs)
	cert, _ := s.ca.agentCert(t, orgX, nodeX)
	stream := s.client(t, &cert, tls.VersionTLS13).Session(within(t))
	if err := stream.Send(helloWithConfig(2)); err != nil {
		t.Fatal(err)
	}
	res, err := receive(stream)
	update := res.GetConfigUpdate()
	if err != nil || update == nil {
		t.Fatalf("Receive() = %v, %v; want ConfigUpdate", res, err)
	}
	logs := update.GetConfig().GetLogs()
	if update.GetVersion() != 3 || !logs.GetEnabled() || logs.GetMinSeverity() != agentv1.LogSeverity_LOG_SEVERITY_WARNING ||
		len(logs.GetExcludeUnits()) != 1 || logs.GetFiles()[0].GetPattern() != "/var/log/nginx/*.log" || logs.GetFiles()[0].GetMultilineStart() != `^\d` ||
		update.GetConfig().GetRedactionRules()[0].GetPattern() != `customer-\d{6}` {
		t.Errorf("ConfigUpdate = %v", update)
	}

	applied := &agentv1.SessionRequest{Payload: &agentv1.SessionRequest_ConfigApplied{ConfigApplied: &agentv1.ConfigApplied{
		Version: 3, Error: "rule 1: look-around is not supported", LogsVetoed: true,
	}}}
	if err := stream.Send(applied); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(configs.answers()) == 1 })
	got := configs.answers()[0]
	if got.org != orgX || got.node != nodeX || got.applied.Version != 3 || !got.applied.LogsVetoed ||
		got.applied.Error != "rule 1: look-around is not supported" || time.Since(got.applied.At) > time.Minute {
		t.Errorf("stored answer = %+v", got)
	}
	_ = stream.CloseRequest()
}

// The same version as the agent's is not sent; a change reaches the sessions of the organization
// once per version, others not.
func TestConfig_ChangesReachTheConnectedAgentsOfTheOrganizationOnce(t *testing.T) {
	configs := &fakeConfigs{configs: map[tenant.OrgID]logconfig.NodeConfig{orgX: nodeConfig(3)}}
	s := startConfigServer(t, configs)
	cert, _ := s.ca.agentCert(t, orgX, nodeX)
	stream := s.client(t, &cert, tls.VersionTLS13).Session(within(t))
	if err := stream.Send(helloWithConfig(3)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(s.sessions.Active()) == 1 })

	s.sessions.ConfigChanged(tenant.OrgID(uuid.New())) // another organization: nothing
	s.sessions.ConfigChanged(orgX)                     // still version 3: nothing
	configs.set(orgX, nodeConfig(4))
	s.sessions.ConfigChanged(orgX)
	res, err := receive(stream)
	if err != nil || res.GetConfigUpdate().GetVersion() != 4 {
		t.Fatalf("Receive() = %v, %v; want ConfigUpdate 4 as the first message after the trust bundle", res, err)
	}
	s.sessions.ConfigChanged(orgX) // version 4 again: not resent
	configs.set(orgX, nodeConfig(5))
	s.sessions.ConfigChanged(orgX)
	if res, err := stream.Receive(); err != nil || res.GetConfigUpdate().GetVersion() != 5 {
		t.Fatalf("Receive() = %v, %v; want ConfigUpdate 5 next", res, err)
	}
	_ = stream.CloseRequest()
}

// Changes made through another server instance arrive with the periodic recheck (T56).
func TestConfig_TheRecheckPicksUpChangesFromOtherInstances(t *testing.T) {
	configs := &fakeConfigs{configs: map[tenant.OrgID]logconfig.NodeConfig{orgX: nodeConfig(1)}}
	s := startServerFull(t, newTestCA(t), 0, nil, []agentapi.HandlerOption{
		agentapi.WithAgentConfig(configs), agentapi.WithCertificateRecheck(50 * time.Millisecond),
	})
	cert, _ := s.ca.agentCert(t, orgX, nodeX)
	stream := s.client(t, &cert, tls.VersionTLS13).Session(within(t))
	if err := stream.Send(helloWithConfig(1)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(s.sessions.Active()) == 1 })
	configs.set(orgX, nodeConfig(2))
	if res, err := receive(stream); err != nil || res.GetConfigUpdate().GetVersion() != 2 {
		t.Fatalf("Receive() = %v, %v; want ConfigUpdate 2 from the recheck", res, err)
	}
	_ = stream.CloseRequest()
}

// The error text comes from the host: control characters become U+FFFD (longer in UTF-8), and the
// result still fits the column (1 KiB).
func TestConfig_TheAgentsErrorIsMadeVisibleAndBounded(t *testing.T) {
	configs := &fakeConfigs{configs: map[tenant.OrgID]logconfig.NodeConfig{orgX: nodeConfig(1)}}
	s := startConfigServer(t, configs)
	cert, _ := s.ca.agentCert(t, orgX, nodeX)
	stream := s.client(t, &cert, tls.VersionTLS13).Session(within(t))
	if err := stream.Send(helloWithConfig(1)); err != nil {
		t.Fatal(err)
	}
	reason := strings.Repeat("\x1b", 1024)
	applied := &agentv1.SessionRequest{Payload: &agentv1.SessionRequest_ConfigApplied{ConfigApplied: &agentv1.ConfigApplied{Version: 1, Error: reason}}}
	if err := stream.Send(applied); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(configs.answers()) == 1 })
	got := configs.answers()[0].applied.Error
	if len(got) > logconfig.MaxErrorBytes || !utf8.ValidString(got) || strings.Contains(got, "\x1b") || got == "" {
		t.Errorf("stored error has %d bytes: %q", len(got), got[:min(len(got), 30)])
	}
	_ = stream.CloseRequest()
}

func withModules(c logconfig.NodeConfig) logconfig.NodeConfig {
	c.Modules = []module.Setting{
		{Module: "nginx", Enabled: true, Interval: 20 * time.Second, Config: `{"url":"http://127.0.0.1/status"}`},
		{Module: "redis", Enabled: true},
		{Module: "simulate", Enabled: false, Config: `{"value":7}`},
	}
	return c
}

// ADR 0039: the node gets its group's modules that its agent has built in (Hello.modules), each
// with its setting; the others it couldn't run.
func TestConfig_ModulesAreThoseOfTheGroupTheAgentHasBuiltIn(t *testing.T) {
	configs := &fakeConfigs{configs: map[tenant.OrgID]logconfig.NodeConfig{orgX: withModules(nodeConfig(3))}}
	s := startConfigServer(t, configs)
	cert, _ := s.ca.agentCert(t, orgX, nodeX)
	stream := s.client(t, &cert, tls.VersionTLS13).Session(within(t))
	hello := helloWithConfig(2)
	hello.GetHello().Modules = []string{"simulate", "nginx"}
	if err := stream.Send(hello); err != nil {
		t.Fatal(err)
	}
	res, err := receive(stream)
	if err != nil || res.GetConfigUpdate() == nil {
		t.Fatalf("Receive() = %v, %v; want ConfigUpdate", res, err)
	}
	mods := res.GetConfigUpdate().GetConfig().GetModules()
	if len(mods) != 2 {
		t.Fatalf("modules = %v; want nginx and simulate", mods)
	}
	nginx, simulate := mods[0], mods[1]
	if nginx.GetId() != "nginx" || !nginx.GetEnabled() || nginx.GetInterval().AsDuration() != 20*time.Second ||
		nginx.GetConfigJson() != `{"url":"http://127.0.0.1/status"}` {
		t.Errorf("nginx = %v", nginx)
	}
	if simulate.GetId() != "simulate" || simulate.GetEnabled() || simulate.GetInterval() != nil || simulate.GetConfigJson() != `{"value":7}` {
		t.Errorf("simulate = %v", simulate)
	}
	configs.mu.Lock()
	announced := configs.announced
	configs.mu.Unlock()
	if len(announced) != 1 || strings.Join(announced[0], ",") != "simulate,nginx" {
		t.Errorf("announced %v", announced)
	}
	_ = stream.CloseRequest()
}

// An agent with other modules built in than before (an update) gets its configuration again,
// even of the same version: the server left out what it couldn't run.
func TestConfig_OtherBuiltInModulesGetTheSameVersionAgain(t *testing.T) {
	configs := &fakeConfigs{configs: map[tenant.OrgID]logconfig.NodeConfig{orgX: withModules(nodeConfig(3))}, changed: true}
	s := startConfigServer(t, configs)
	cert, _ := s.ca.agentCert(t, orgX, nodeX)
	stream := s.client(t, &cert, tls.VersionTLS13).Session(within(t))
	hello := helloWithConfig(3)
	hello.GetHello().Modules = []string{"nginx"}
	if err := stream.Send(hello); err != nil {
		t.Fatal(err)
	}
	res, err := receive(stream)
	if err != nil || res.GetConfigUpdate().GetVersion() != 3 || len(res.GetConfigUpdate().GetConfig().GetModules()) != 1 {
		t.Fatalf("Receive() = %v, %v; want ConfigUpdate 3 with nginx", res, err)
	}
	_ = stream.CloseRequest()
}

func TestConfig_TheModulesVetoIsStored(t *testing.T) {
	configs := &fakeConfigs{configs: map[tenant.OrgID]logconfig.NodeConfig{orgX: nodeConfig(1)}}
	s := startConfigServer(t, configs)
	cert, _ := s.ca.agentCert(t, orgX, nodeX)
	stream := s.client(t, &cert, tls.VersionTLS13).Session(within(t))
	if err := stream.Send(helloWithConfig(1)); err != nil {
		t.Fatal(err)
	}
	applied := &agentv1.SessionRequest{Payload: &agentv1.SessionRequest_ConfigApplied{ConfigApplied: &agentv1.ConfigApplied{Version: 1, ModulesVetoed: true}}}
	if err := stream.Send(applied); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(configs.answers()) == 1 })
	if got := configs.answers()[0].applied; !got.ModulesVetoed || got.LogsVetoed {
		t.Errorf("stored answer = %+v", got)
	}
	_ = stream.CloseRequest()
}

// testBox seals like the server's master keys.
func testBox(t *testing.T) *secrets.Box {
	t.Helper()
	key, err := secrets.NewKey(1, bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.NewBox(key)
	if err != nil {
		t.Fatal(err)
	}
	return box
}

var groupX = tenant.GroupID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000cc"))

func sealed(t *testing.T, box *secrets.Box, org tenant.OrgID, group tenant.GroupID, mod, field, value string) module.SealedSecret {
	t.Helper()
	s, err := box.Seal([]byte(value), module.SecretContext(org, group, mod, field))
	if err != nil {
		t.Fatal(err)
	}
	return module.SealedSecret{Group: group, Module: mod, Field: field, Sealed: s}
}

// M5-05: after the Hello – also with the agent's version current – the agent gets the opened
// credentials of the modules it has built in, before any configuration; a value sealed for
// another group or organization doesn't open and stays out; every new version brings them again.
// M5-33: only of modules that are on – a module turned off takes its credentials off the agents.
func TestConfig_TheCredentialsOfTheModulesGoToTheAgent(t *testing.T) {
	box := testBox(t)
	const value = "pg-value-for-x" // betterleaks:allow
	config := withModules(nodeConfig(3))
	config.Modules = []module.Setting{{Module: "simulate", Enabled: true}, {Module: "nginx", Enabled: false}}
	config.Secrets = []module.SealedSecret{
		sealed(t, box, orgX, groupX, "nginx", "password", "module-off"),
		sealed(t, box, orgX, groupX, "simulate", "token", value),
		sealed(t, box, orgX, groupX, "simulate", "user_key", "second-value"),
		sealed(t, box, orgX, groupX, "redis", "password", "not-built-in"),
		sealed(t, box, tenant.OrgID(uuid.New()), groupX, "simulate", "other", "foreign-org"),
		{Group: groupX, Module: "simulate", Field: "broken", Sealed: []byte("garbage")},
	}
	configs := &fakeConfigs{configs: map[tenant.OrgID]logconfig.NodeConfig{orgX: config}}
	s := startServerFull(t, newTestCA(t), 0, nil, []agentapi.HandlerOption{agentapi.WithAgentConfig(configs), agentapi.WithModuleSecrets(box)})
	cert, _ := s.ca.agentCert(t, orgX, nodeX)
	stream := s.client(t, &cert, tls.VersionTLS13).Session(within(t))
	hello := helloWithConfig(3) // current: no ConfigUpdate, but the credentials
	hello.GetHello().Modules = []string{"simulate", "nginx", "redis"}
	if err := stream.Send(hello); err != nil {
		t.Fatal(err)
	}
	res, err := receive(stream)
	got := res.GetModuleSecrets()
	if err != nil || got == nil {
		t.Fatalf("Receive() = %v, %v; want ModuleSecrets", res, err)
	}
	if len(got.GetSecrets()) != 2 || got.GetSecrets()[0].GetModuleId() != "simulate" || got.GetSecrets()[0].GetField() != "token" ||
		got.GetSecrets()[0].GetValue() != value || got.GetSecrets()[1].GetField() != "user_key" || got.GetSecrets()[1].GetValue() != "second-value" {
		t.Fatalf("secrets = %d entries", len(got.GetSecrets()))
	}

	configs.set(orgX, func() logconfig.NodeConfig { c := config; c.Version = 4; return c }())
	s.sessions.ConfigChanged(orgX)
	first, err := stream.Receive()
	if err != nil || first.GetModuleSecrets() == nil {
		t.Fatalf("Receive() = %v, %v; want the credentials before the configuration", first, err)
	}
	if next, err := stream.Receive(); err != nil || next.GetConfigUpdate().GetVersion() != 4 {
		t.Fatalf("Receive() = %v, %v; want ConfigUpdate 4", next, err)
	}
	_ = stream.CloseRequest()
}

// Without credentials an empty set goes out once: it clears what an agent still holds.
func TestConfig_AnEmptySetOfCredentialsGoesOutOnce(t *testing.T) {
	configs := &fakeConfigs{configs: map[tenant.OrgID]logconfig.NodeConfig{orgX: nodeConfig(1)}}
	s := startServerFull(t, newTestCA(t), 0, nil, []agentapi.HandlerOption{agentapi.WithAgentConfig(configs), agentapi.WithModuleSecrets(testBox(t))})
	cert, _ := s.ca.agentCert(t, orgX, nodeX)
	stream := s.client(t, &cert, tls.VersionTLS13).Session(within(t))
	if err := stream.Send(helloWithConfig(1)); err != nil {
		t.Fatal(err)
	}
	res, err := receive(stream)
	if err != nil || res.GetModuleSecrets() == nil || len(res.GetModuleSecrets().GetSecrets()) != 0 {
		t.Fatalf("Receive() = %v, %v", res, err)
	}
	s.sessions.ConfigChanged(orgX) // same version: nothing
	configs.set(orgX, nodeConfig(2))
	s.sessions.ConfigChanged(orgX)
	if next, err := stream.Receive(); err != nil || next.GetModuleSecrets() == nil {
		t.Fatalf("Receive() = %v, %v; want the credentials with version 2", next, err)
	}
	if next, err := stream.Receive(); err != nil || next.GetConfigUpdate().GetVersion() != 2 {
		t.Fatalf("Receive() = %v, %v", next, err)
	}
	_ = stream.CloseRequest()
}
