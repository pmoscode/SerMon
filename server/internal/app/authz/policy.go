// Package authz is the central authorization policy (KONZEPT §7, §18.3): deny by default.
//
// Every use case asks the policy before it acts. Group-scoped actions need the required role on
// the target node group or on all groups ("*"); organization-wide actions need it on "*".
// The superadmin manages organizations but sees no tenant data without a membership.
package authz

import (
	"errors"

	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// UserID identifies the acting user.
type UserID = identity.UserID

// Action is something a principal wants to do.
type Action uint16

// Actions. Keep rules in sync with KONZEPT §7 and the role-matrix test.
const (
	actionUnknown Action = iota

	// Group-scoped actions.
	ViewNodes              // nodes, metrics, dashboards
	ViewLogs               // logs of nodes
	ViewSecurityEvents     // ssh logins, sudo, ...
	UseLiveView            // live process view
	AcknowledgeAlerts      // acknowledge alerts
	SilenceAlerts          // create silences
	RunUpdates             // trigger package updates
	RebootNodes            // trigger reboots
	CreateEnrollmentTokens // add nodes to a group
	RotateNodeKeys         // on-demand key rotation (KONZEPT §6)

	// Organization-wide actions (Admin on all groups).
	ManageAlertRules
	ManageChannels
	ManagePolicies
	ManageModules
	ManageMembers
	ManageGroups
	ViewAuditLog

	// Instance-level actions (superadmin).
	ManageOrganizations
	ManageAgentCA     // agent CA rollover (KONZEPT §6)
	ViewInstanceAudit // instance-wide audit log

	actionEnd
)

type rule struct {
	name    string
	minimum role.Role
	scope   scopeKind
	// viewerLogs marks actions a viewer only gets while the org allows logs for viewers.
	viewerLogs bool
}

type scopeKind uint8

const (
	groupScoped scopeKind = iota + 1
	orgWide
	instanceLevel
)

var rules = map[Action]rule{
	ViewNodes:              {name: "view_nodes", minimum: role.Viewer, scope: groupScoped},
	ViewLogs:               {name: "view_logs", minimum: role.Viewer, scope: groupScoped, viewerLogs: true},
	ViewSecurityEvents:     {name: "view_security_events", minimum: role.Viewer, scope: groupScoped, viewerLogs: true},
	UseLiveView:            {name: "use_live_view", minimum: role.Viewer, scope: groupScoped},
	AcknowledgeAlerts:      {name: "acknowledge_alerts", minimum: role.Operator, scope: groupScoped},
	SilenceAlerts:          {name: "silence_alerts", minimum: role.Operator, scope: groupScoped},
	RunUpdates:             {name: "run_updates", minimum: role.Operator, scope: groupScoped},
	RebootNodes:            {name: "reboot_nodes", minimum: role.Operator, scope: groupScoped},
	CreateEnrollmentTokens: {name: "create_enrollment_tokens", minimum: role.Operator, scope: groupScoped},
	RotateNodeKeys:         {name: "rotate_node_keys", minimum: role.Operator, scope: groupScoped},
	ManageAlertRules:       {name: "manage_alert_rules", minimum: role.Admin, scope: orgWide},
	ManageChannels:         {name: "manage_channels", minimum: role.Admin, scope: orgWide},
	ManagePolicies:         {name: "manage_policies", minimum: role.Admin, scope: orgWide},
	ManageModules:          {name: "manage_modules", minimum: role.Admin, scope: orgWide},
	ManageMembers:          {name: "manage_members", minimum: role.Admin, scope: orgWide},
	ManageGroups:           {name: "manage_groups", minimum: role.Admin, scope: orgWide},
	ViewAuditLog:           {name: "view_audit_log", minimum: role.Admin, scope: orgWide},
	ManageOrganizations:    {name: "manage_organizations", scope: instanceLevel},
	ManageAgentCA:          {name: "manage_agent_ca", scope: instanceLevel},
	ViewInstanceAudit:      {name: "view_instance_audit", scope: instanceLevel},
}

// AllActions returns all tenant actions (group-scoped and organization-wide).
func AllActions() []Action {
	var actions []Action
	for a := actionUnknown + 1; a < actionEnd; a++ {
		if rules[a].scope != instanceLevel {
			actions = append(actions, a)
		}
	}
	return actions
}

// String returns the action name (for logs and audit entries).
func (a Action) String() string {
	if r, ok := rules[a]; ok {
		return r.name
	}
	return "unknown"
}

// Binding grants a role on one node group or on all groups of the principal's organization.
type Binding struct {
	Group     tenant.GroupID
	AllGroups bool
	Role      role.Role
}

// Principal is the authenticated caller within one organization.
type Principal struct {
	User       UserID
	Org        tenant.OrgID // zero for a superadmin acting at instance level
	Bindings   []Binding
	Superadmin bool
}

// Target is the object of an action: an organization or a node group in it.
type Target struct {
	org      tenant.OrgID
	group    tenant.GroupID
	hasGroup bool
}

// InOrg targets the organization as a whole.
func InOrg(org tenant.OrgID) Target { return Target{org: org} }

// InGroup targets a node group of an organization.
func InGroup(org tenant.OrgID, group tenant.GroupID) Target {
	return Target{org: org, group: group, hasGroup: true}
}

// Policy decides authorization requests.
type Policy struct {
	viewerLogsDisabled map[tenant.OrgID]bool
}

// Option configures a Policy.
type Option func(*Policy)

// WithViewerLogsDisabled denies logs and security events to viewers of the organization
// (logs contain personal data, KONZEPT §7).
func WithViewerLogsDisabled(org tenant.OrgID) Option {
	return func(p *Policy) { p.viewerLogsDisabled[org] = true }
}

// NewPolicy creates a policy.
func NewPolicy(opts ...Option) Policy {
	p := Policy{viewerLogsDisabled: map[tenant.OrgID]bool{}}
	for _, opt := range opts {
		opt(&p)
	}
	return p
}

// Allowed reports whether p may perform action on target. Anything not explicitly allowed is denied.
func (pol Policy) Allowed(p Principal, action Action, target Target) bool {
	r, ok := rules[action]
	if !ok || p.User.IsZero() || isZeroOrg(p.Org) || target.org != p.Org {
		return false
	}

	var held role.Role
	switch r.scope {
	case groupScoped:
		if !target.hasGroup {
			return false
		}
		held = p.roleFor(target.group)
	case orgWide:
		held = p.roleOnAllGroups()
	case instanceLevel:
		return false // instance actions are decided by AllowedInstance only
	}

	minimum := r.minimum
	if r.viewerLogs && pol.viewerLogsDisabled[p.Org] {
		minimum = role.Operator
	}
	return held.AtLeast(minimum)
}

// AllowedInstance reports whether p may perform an instance-level action.
func (pol Policy) AllowedInstance(p Principal, action Action) bool {
	r, ok := rules[action]
	return ok && r.scope == instanceLevel && p.Superadmin && !p.User.IsZero()
}

// roleFor returns the highest role of p on the group (direct binding or "*").
func (p Principal) roleFor(group tenant.GroupID) role.Role {
	var best role.Role
	for _, b := range p.Bindings {
		if (b.AllGroups || b.Group == group) && b.Role.Valid() && b.Role > best {
			best = b.Role
		}
	}
	return best
}

// roleOnAllGroups returns the highest role of p bound to all groups.
func (p Principal) roleOnAllGroups() role.Role {
	var best role.Role
	for _, b := range p.Bindings {
		if b.AllGroups && b.Role.Valid() && b.Role > best {
			best = b.Role
		}
	}
	return best
}

// ErrNoOrganization means the principal does not act within an organization.
var ErrNoOrganization = errors.New("principal is not acting within an organization")

// ScopeFor returns the tenant scope of all node groups on which p holds at least minimum.
// Use it to build list queries ("all nodes the user may see").
func ScopeFor(p Principal, minimum role.Role) (tenant.Scope, error) {
	if isZeroOrg(p.Org) || p.User.IsZero() {
		return tenant.Scope{}, ErrNoOrganization
	}
	if p.roleOnAllGroups().AtLeast(minimum) {
		return tenant.ForAllGroups(p.Org)
	}
	var groups []tenant.GroupID
	for _, b := range p.Bindings {
		if !b.AllGroups && b.Role.AtLeast(minimum) {
			groups = append(groups, b.Group)
		}
	}
	return tenant.ForGroups(p.Org, groups...)
}

func isZeroOrg(org tenant.OrgID) bool { return org == tenant.OrgID{} }
