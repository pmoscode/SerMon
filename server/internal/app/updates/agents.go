package updates

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/command"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	"github.com/pmoscode/SerMon/server/internal/domain/update"
)

// Releases are the signed agent releases the server offers (ADR 0036).
type Releases interface {
	// Latest returns the version of channel for arch; false if there is none.
	Latest(channel update.Channel, arch string) (string, bool)
}

// WithReleases lets the service update agents and show their releases.
func (s *Service) WithReleases(r Releases) *Service {
	s.releases = r
	return s
}

// AgentState is a node's agent and what its group's channel offers.
type AgentState struct {
	update.Agent
	Channel update.Channel
	// Latest is the newest release of the channel for the node's architecture ("" if none);
	// Target the version a self-update installs ("" if none: not newer, or not a script install).
	Latest, Target string
}

func (s *Service) agentState(ctx context.Context, org tenant.OrgID, n NodeUpdates) (AgentState, error) {
	out := AgentState{Agent: n.Agent, Channel: update.ChannelStable}
	if s.policies != nil {
		scope, err := tenant.ForAllGroups(org)
		if err != nil {
			return AgentState{}, access.ErrForbidden
		}
		p, ok, err := s.policies.Policy(ctx, scope, n.Group)
		if err != nil {
			return AgentState{}, fmt.Errorf("read update policy: %w", err)
		}
		if ok {
			out.Channel = p.Channel
		}
	}
	if s.releases != nil {
		out.Latest, _ = s.releases.Latest(out.Channel, n.Agent.Arch)
	}
	out.Target = n.Agent.Target(out.Latest)
	return out, nil
}

// AgentState returns the agent of a node the caller views (view_nodes) with its channel's release.
func (s *Service) AgentState(ctx context.Context, c access.Caller, n NodeUpdates) (AgentState, error) {
	return s.agentState(ctx, c.Principal.Org, n)
}

// Releases returns the versions of the channels per architecture (view_nodes; for the policies).
func (s *Service) Releases(c access.Caller) (map[update.Channel]map[string]string, error) {
	if _, err := s.scope(c); err != nil {
		return nil, err
	}
	out := map[update.Channel]map[string]string{}
	if s.releases == nil {
		return out, nil
	}
	for _, c := range []update.Channel{update.ChannelStable, update.ChannelBeta} {
		for _, arch := range []string{"amd64", "arm64"} {
			if v, ok := s.releases.Latest(c, arch); ok {
				if out[c] == nil {
					out[c] = map[string]string{}
				}
				out[c][arch] = v
			}
		}
	}
	return out, nil
}

// Skipped is a node an agent update leaves out, with the reason.
type Skipped struct {
	Node   identity.NodeID
	Reason string
}

// UpdateAgents updates the agents of the nodes to the newest release of their group's channel
// (run_updates on their groups, a fresh step-up; ADR 0036). Nodes that are installed by a
// package, have no newer release or forbid it are skipped with the reason.
func (s *Service) UpdateAgents(ctx context.Context, c access.Caller, ids []identity.NodeID) ([]command.Command, []Skipped, error) {
	nodes, err := s.visibleNodes(ctx, c, ids, authz.RunUpdates)
	if err != nil {
		return nil, nil, err
	}
	now := s.now()
	if now.Sub(c.StepUpAt) > access.StepUpMaxAge {
		return nil, nil, access.ErrStepUpRequired
	}
	if s.manual == nil {
		return nil, nil, errors.New("no manual store")
	}
	var commands []command.Command
	var skipped []Skipped
	var names []string
	for _, n := range nodes {
		state, err := s.agentState(ctx, c.Principal.Org, n)
		if err != nil {
			return nil, nil, err
		}
		reason := ""
		switch {
		case n.Agent.Method == update.InstallPackage:
			reason = "installed by a package: the package manager updates it"
		case n.Agent.Method != update.InstallScript:
			reason = "not installed by install.sh"
		case n.Report != nil && n.Report.Permissions != nil && !n.Report.Permissions.SelfUpdate:
			reason = "the host forbids self-updates (allow_self_update)"
		case state.Target == "":
			reason = "no newer release in the channel " + state.Channel.String()
		}
		if reason != "" {
			skipped = append(skipped, Skipped{Node: n.Node, Reason: reason})
			continue
		}
		commands = append(commands, command.Command{
			ID: s.newID(), Node: n.Node, Kind: command.SelfUpdate, Version: state.Target, CreatedAt: now,
			ExpiresAt: now.Add(command.DefaultExpiry), CreatedBy: c.Principal.User,
		})
		names = append(names, n.Node.String()+"="+state.Target)
	}
	if len(commands) == 0 {
		return nil, skipped, nil
	}
	audit := access.AuditEntry{
		Actor: c.Principal.User, Action: "agents.update", Target: "nodes:" + strconv.Itoa(len(commands)),
		Details: map[string]string{"nodes": strings.Join(names, ",")},
	}
	if err := s.manual.Enqueue(ctx, c.Principal.Org, commands, audit); err != nil {
		return nil, nil, fmt.Errorf("update agents: %w", err)
	}
	return commands, skipped, nil
}
