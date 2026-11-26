package agentapi

import (
	"context"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/pmoscode/SerMon/server/internal/app/logsettings"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/logconfig"
	"github.com/pmoscode/SerMon/server/internal/domain/module"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	"github.com/pmoscode/SerMon/server/internal/domain/textsafe"
	agentv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/agent/v1"
)

// AgentConfigs provides the configuration of a node and stores the agent's answer (ADR 0030).
// Organization and node come from the certificate.
type AgentConfigs interface {
	ForNode(ctx context.Context, org tenant.OrgID, node identity.NodeID) (logconfig.NodeConfig, error)
	Applied(ctx context.Context, org tenant.OrgID, node identity.NodeID, applied logconfig.Applied) error
	// Announced stores the modules the agent has built in (Hello.modules, ADR 0039) and reports
	// whether they differ from the stored ones.
	Announced(ctx context.Context, org tenant.OrgID, node identity.NodeID, modules []string) (bool, error)
}

// WithAgentConfig sends the server's configuration to the agents (M3-03); without it they keep
// what they have.
func WithAgentConfig(configs AgentConfigs) HandlerOption {
	return func(h *SessionHandler) { h.configs = configs }
}

// SecretOpener opens the sealed credentials of the modules (secrets.Box).
type SecretOpener interface {
	Open(sealed, associatedData []byte) ([]byte, error)
}

// WithModuleSecrets sends the credentials of the modules to the agents (M5-05, ADR 0039),
// opened with opener; without it they get none.
func WithModuleSecrets(opener SecretOpener) HandlerOption {
	return func(h *SessionHandler) { h.secrets = opener }
}

// maxModuleSecrets bounds a ModuleSecrets message (agent.proto).
const maxModuleSecrets = 64

var _ logsettings.Agents = (*Sessions)(nil)

// ConfigChanged tells the sessions of the organization on this server that its configuration
// changed (logsettings.Agents); other instances notice at their next recheck.
func (s *Sessions) ConfigChanged(org tenant.OrgID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.active {
		if l.Org != org {
			continue
		}
		select {
		case l.config <- struct{}{}:
		default: // a check is already queued
		}
	}
}

// configState is what a session knows about the agent's configuration.
type configState struct {
	agent   uint64   // the version from the Hello
	sent    uint64   // the version last sent in this session
	modules []string // the modules the agent has built in (Hello)
	// secretsSent: the credentials went out in this session (after every Hello once).
	secretsSent bool
}

// newConfigState records what the Hello says. An agent with other modules built in than before
// (an update) gets its configuration again, of the same version too: the server left out what
// it couldn't run (ADR 0039).
func (h *SessionHandler) newConfigState(ctx context.Context, id Identity, hello *agentv1.Hello) *configState {
	state := &configState{agent: hello.GetConfigVersion(), modules: hello.GetModules()}
	if h.configs == nil {
		return state
	}
	changed, err := h.configs.Announced(ctx, id.Org, id.Node, state.modules)
	if err != nil {
		h.logger.WarnContext(ctx, "record built-in modules failed", "node", id.Node.String(), "error", err)
	}
	if changed {
		state.agent = 0
	}
	return state
}

// sendConfig sends the node's configuration if its version is neither the agent's nor the one
// sent last: a configuration the agent rejected goes out once per session, not at every check.
func (h *SessionHandler) sendConfig(ctx context.Context, id Identity, state *configState, stream *connect.BidiStream[agentv1.SessionRequest, agentv1.SessionResponse]) error {
	if h.configs == nil {
		return nil
	}
	config, err := h.configs.ForNode(ctx, id.Org, id.Node)
	if err != nil {
		// The agent keeps its configuration; the next check tries again.
		h.logger.WarnContext(ctx, "load agent config failed", "node", id.Node.String(), "error", err)
		return nil
	}
	changed := config.Version != state.agent && config.Version != state.sent
	// The credentials first, so the modules of the new configuration start with them; once per
	// session (the agent keeps them in memory only) and with every configuration.
	if h.secrets != nil && (changed || !state.secretsSent) {
		msg := h.moduleSecrets(ctx, id, config, state.modules)
		if err := stream.Send(&agentv1.SessionResponse{Payload: &agentv1.SessionResponse_ModuleSecrets{ModuleSecrets: msg}}); err != nil {
			return err //nolint:wrapcheck // connect errors must reach the client unchanged
		}
		state.secretsSent = true
	}
	if !changed {
		return nil
	}
	if err := stream.Send(&agentv1.SessionResponse{Payload: &agentv1.SessionResponse_ConfigUpdate{ConfigUpdate: configUpdate(config, state.modules)}}); err != nil {
		return err //nolint:wrapcheck // connect errors must reach the client unchanged
	}
	state.sent = config.Version
	h.logger.InfoContext(ctx, "agent config sent", "node", id.Node.String(), "version", config.Version)
	return nil
}

// moduleSecrets opens the credentials of the node's group for the modules its agent has built
// in and that are on (M5-33): a module turned off takes its credentials off the agents. A value
// that doesn't open (a key gone, a copied record) is left out and logged without it.
func (h *SessionHandler) moduleSecrets(ctx context.Context, id Identity, config logconfig.NodeConfig, builtin []string) *agentv1.ModuleSecrets {
	running := map[string]bool{}
	for _, m := range module.ForAgent(config.Modules, builtin) {
		running[m.Module] = m.Enabled
	}
	msg := &agentv1.ModuleSecrets{}
	for _, s := range config.Secrets {
		if !running[s.Module] || len(msg.GetSecrets()) >= maxModuleSecrets {
			continue
		}
		value, err := h.secrets.Open(s.Sealed, module.SecretContext(id.Org, s.Group, s.Module, s.Field))
		if err != nil {
			h.logger.WarnContext(ctx, "a credential of a module does not open, left out", "node", id.Node.String(), "module", s.Module, "field", s.Field)
			continue
		}
		msg.Secrets = append(msg.Secrets, &agentv1.ModuleSecret{ModuleId: s.Module, Field: s.Field, Value: string(value)})
	}
	return msg
}

// recordApplied stores the agent's answer; the error text comes from the host.
func (h *SessionHandler) recordApplied(ctx context.Context, id Identity, applied *agentv1.ConfigApplied) {
	if h.configs == nil {
		return
	}
	answer := logconfig.Applied{
		Version: applied.GetVersion(), Error: cutUTF8(textsafe.Visible(applied.GetError()), logconfig.MaxErrorBytes),
		LogsVetoed: applied.GetLogsVetoed(), ModulesVetoed: applied.GetModulesVetoed(), At: time.Now(),
	}
	if answer.Error != "" {
		h.logger.WarnContext(ctx, "agent rejected its config", "node", id.Node.String(), "version", answer.Version, "error", answer.Error)
	}
	if err := h.configs.Applied(ctx, id.Org, id.Node, answer); err != nil {
		h.logger.WarnContext(ctx, "record agent config answer failed", "node", id.Node.String(), "error", err)
	}
}

func configUpdate(c logconfig.NodeConfig, builtin []string) *agentv1.ConfigUpdate {
	logs := &agentv1.LogConfig{
		Enabled:      c.Settings.Enabled,
		MinSeverity:  agentv1.LogSeverity(c.Settings.MinSeverity), // same codes (logconfig.Severity)
		ExcludeUnits: c.Settings.ExcludeUnits,
	}
	for _, f := range c.Settings.Files {
		logs.Files = append(logs.Files, &agentv1.LogFileGlob{Pattern: f.Pattern, MultilineStart: f.MultilineStart})
	}
	config := &agentv1.AgentConfig{Logs: logs}
	for _, r := range c.Rules {
		config.RedactionRules = append(config.RedactionRules, &agentv1.RedactionRule{Pattern: r.Pattern})
	}
	for _, m := range module.ForAgent(c.Modules, builtin) {
		mc := &agentv1.ModuleConfig{Id: m.Module, Enabled: m.Enabled, ConfigJson: m.Config}
		if m.Interval > 0 {
			mc.Interval = durationpb.New(m.Interval)
		}
		config.Modules = append(config.Modules, mc)
	}
	return &agentv1.ConfigUpdate{Version: c.Version, Config: config}
}

// cutUTF8 shortens s to at most max bytes without splitting a character.
func cutUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}
